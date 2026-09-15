package service

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
)

const (
	ticketWordPreviewMaxInput = 20 << 20
	ticketWordPreviewMaxChars = 2 << 20
	ticketWordPreviewMaxBytes = 8 << 20
)

// 文本提取也必须限制并发，避免多个历史文档同时解压导致内存峰值失控。
var ticketWordPreviewSlots = make(chan struct{}, 2)

// PreviewTicketWord 重新检查历史附件后提取正文纯文本，不渲染、执行或联网加载文档内容。
// @project-doc docs/domains/support_tickets.md#ticket_attachments
func PreviewTicketWord(data []byte, contentType string) (string, error) {
	if len(data) == 0 || len(data) > ticketWordPreviewMaxInput {
		return "", ticketAttachmentInvalid("Word 预览文件不能为空或超过 20 MiB")
	}
	select {
	case ticketWordPreviewSlots <- struct{}{}:
		defer func() { <-ticketWordPreviewSlots }()
	default:
		return "", ErrTicketAttachmentBusy
	}
	var text string
	var err error
	switch contentType {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		if err = validateTicketDocx(data); err == nil {
			text, err = previewTicketDOCX(data)
		}
	case "application/msword":
		if err = validateTicketDOC(data); err == nil {
			text, err = previewTicketDOC(data)
		}
	default:
		return "", ticketAttachmentInvalid("仅支持 DOC 和 DOCX 文本预览")
	}
	if err != nil {
		return "", infraerrors.BadRequest("TICKET_ATTACHMENT_PREVIEW_UNAVAILABLE", "无法安全预览该 Word 文档："+err.Error())
	}
	return strings.TrimSpace(text), nil
}

// previewTicketDOCX 只处理已通过完整 ZIP/XML 校验的正文；表格单元格用制表符分隔。
func previewTicketDOCX(data []byte) (string, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("DOCX 文件结构无效")
	}
	for _, entry := range r.File {
		if entry.Name != "word/document.xml" {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return "", fmt.Errorf("DOCX 正文无法读取")
		}
		defer rc.Close()
		decoder := xml.NewDecoder(io.LimitReader(rc, ticketMaxDocxXMLBytes+1))
		var out strings.Builder
		inText, inBody, cellDepth := 0, 0, 0
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				return out.String(), nil
			}
			if err != nil {
				return "", fmt.Errorf("DOCX 正文 XML 无效")
			}
			var value string
			switch token := token.(type) {
			case xml.StartElement:
				if !ticketWordXMLNamespace(token.Name.Space) {
					continue
				}
				if token.Name.Local == "body" {
					inBody++
				}
				if inBody == 0 {
					continue
				}
				switch token.Name.Local {
				case "t":
					inText++
				case "tc":
					cellDepth++
				case "tab":
					value = "\t"
				case "br", "cr":
					value = "\n"
				}
			case xml.EndElement:
				if !ticketWordXMLNamespace(token.Name.Space) || inBody == 0 {
					continue
				}
				switch token.Name.Local {
				case "body":
					inBody--
				case "t":
					inText--
				case "tc":
					cellDepth--
					value = "\t"
				case "tr":
					value = "\n"
				case "p":
					if cellDepth == 0 {
						value = "\n"
					} else {
						value = " "
					}
				}
			case xml.CharData:
				if inBody > 0 && inText > 0 {
					value = string(token)
				}
			}
			if out.Len()+len(value) > ticketWordPreviewMaxBytes {
				return "", fmt.Errorf("正文超过 8 MiB 文本预览上限")
			}
			out.WriteString(value)
		}
	}
	return "", fmt.Errorf("DOCX 缺少正文")
}

func ticketWordXMLNamespace(space string) bool {
	return space == "http://schemas.openxmlformats.org/wordprocessingml/2006/main" || space == "http://purl.oclc.org/ooxml/wordprocessingml/main"
}

// ticketDOCContainer 仅提供受限流读取，不实现可执行对象或其他 Office 文档功能。
type ticketDOCContainer struct {
	data       []byte
	sectorSize int
	fat        []uint32
	used       map[uint32]bool
}

const ticketDOCEnd uint32 = 0xfffffffe
const ticketDOCFree uint32 = 0xffffffff

func (c *ticketDOCContainer) sector(id uint32) ([]byte, error) {
	if uint64(id) >= uint64(len(c.data)/c.sectorSize-1) {
		return nil, fmt.Errorf("DOC 扇区超出文件范围")
	}
	start := (int(id) + 1) * c.sectorSize
	return c.data[start : start+c.sectorSize], nil
}

// chain 拒绝循环、跨流重叠、额外链尾及尺寸伪造；size=-1 仅用于未知长度的目录流。
func (c *ticketDOCContainer) chain(start uint32, size int64, limit int) ([]byte, error) {
	if size > int64(limit) || size < -1 {
		return nil, fmt.Errorf("DOC 流大小超限")
	}
	var out []byte
	for id := start; id != ticketDOCEnd; id = c.fat[id] {
		if int64(len(out)) >= size && size >= 0 || len(out) >= limit || uint64(id) >= uint64(len(c.fat)) || c.used[id] {
			return nil, fmt.Errorf("DOC 流链循环、重叠或长度无效")
		}
		block, err := c.sector(id)
		if err != nil {
			return nil, err
		}
		c.used[id] = true
		out = append(out, block...)
	}
	if size >= 0 {
		if int64(len(out)) < size {
			return nil, fmt.Errorf("DOC 流内容不完整")
		}
		out = out[:size]
	}
	return out, nil
}

// readTicketDOCStreams 在现有危险内容检查之后验证并提取主文档及表流，覆盖 MiniFAT 小流。
func readTicketDOCStreams(data []byte) (map[string][]byte, error) {
	le := binary.LittleEndian
	c := ticketDOCContainer{data: data, sectorSize: 1 << le.Uint16(data[30:32]), used: make(map[uint32]bool)}
	var fatIDs []uint32
	for offset := 76; offset < 512; offset += 4 {
		if id := le.Uint32(data[offset : offset+4]); id != ticketDOCFree {
			fatIDs = append(fatIDs, id)
		}
	}
	id := le.Uint32(data[68:72])
	for i := uint32(0); i < le.Uint32(data[72:76]); i++ {
		block, err := c.sector(id)
		if err != nil || c.used[id] {
			return nil, fmt.Errorf("DOC DIFAT 流无效")
		}
		c.used[id] = true
		for offset := 0; offset < c.sectorSize-4; offset += 4 {
			if fatID := le.Uint32(block[offset : offset+4]); fatID != ticketDOCFree {
				fatIDs = append(fatIDs, fatID)
			}
		}
		id = le.Uint32(block[c.sectorSize-4:])
	}
	if len(fatIDs) != int(le.Uint32(data[44:48])) {
		return nil, fmt.Errorf("DOC FAT 数量不一致")
	}
	for _, id := range fatIDs {
		block, err := c.sector(id)
		if err != nil || c.used[id] {
			return nil, fmt.Errorf("DOC FAT 流重叠")
		}
		c.used[id] = true
		for offset := 0; offset < c.sectorSize; offset += 4 {
			c.fat = append(c.fat, le.Uint32(block[offset:offset+4]))
		}
	}
	directory, err := c.chain(le.Uint32(data[48:52]), -1, 2<<20)
	if err != nil || len(directory) < 128 {
		return nil, fmt.Errorf("DOC 目录无法读取")
	}
	rootSize := le.Uint64(directory[120:128])
	if rootSize > uint64(len(data)) {
		return nil, fmt.Errorf("DOC 小流大小超限")
	}
	root, err := c.chain(le.Uint32(directory[116:120]), int64(rootSize), len(data))
	if err != nil {
		return nil, err
	}
	miniCount := le.Uint32(data[64:68])
	miniData, err := c.chain(le.Uint32(data[60:64]), int64(miniCount)*int64(c.sectorSize), len(data))
	if err != nil {
		return nil, err
	}
	miniUsed := make(map[uint32]bool)
	streams := make(map[string][]byte)
	queue := []uint32{le.Uint32(directory[76:80])}
	visited := make(map[uint32]bool)
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if id == ticketDOCFree {
			continue
		}
		if uint64(id) >= uint64(len(directory)/128) || visited[id] {
			return nil, fmt.Errorf("DOC 目录树无效")
		}
		visited[id] = true
		entry := directory[int(id)*128 : (int(id)+1)*128]
		queue = append(queue, le.Uint32(entry[68:72]), le.Uint32(entry[72:76]))
		if entry[66] != 2 {
			return nil, fmt.Errorf("DOC 根目录含有非文档流")
		}
		nameLength := int(le.Uint16(entry[64:66]))
		if nameLength < 2 || nameLength > 64 || nameLength%2 != 0 || le.Uint16(entry[nameLength-2:nameLength]) != 0 {
			return nil, fmt.Errorf("DOC 流名称无效")
		}
		nameUnits := make([]uint16, nameLength/2-1)
		for i := range nameUnits {
			nameUnits[i] = le.Uint16(entry[2*i : 2*i+2])
		}
		name := string(utf16.Decode(nameUnits))
		if _, duplicate := streams[name]; duplicate {
			return nil, fmt.Errorf("DOC 流名称重复")
		}
		size := le.Uint64(entry[120:128])
		if size > uint64(len(data)) {
			return nil, fmt.Errorf("DOC 流大小超限")
		}
		start := le.Uint32(entry[116:120])
		var stream []byte
		if size >= 4096 {
			stream, err = c.chain(start, int64(size), len(data))
			if err != nil {
				return nil, err
			}
		} else {
			for miniID := start; miniID != ticketDOCEnd; {
				if uint64(len(stream)) >= size || uint64(miniID)*4+4 > uint64(len(miniData)) || uint64(miniID)*64+64 > uint64(len(root)) || miniUsed[miniID] {
					return nil, fmt.Errorf("DOC 小流链循环、重叠或越界")
				}
				miniUsed[miniID] = true
				stream = append(stream, root[int(miniID)*64:int(miniID)*64+64]...)
				miniID = le.Uint32(miniData[int(miniID)*4 : int(miniID)*4+4])
			}
			if uint64(len(stream)) < size {
				return nil, fmt.Errorf("DOC 小流内容不完整")
			}
			stream = stream[:size]
		}
		// 其他元数据流完成链校验后即可释放，避免不必要地长期保留内容。
		streams[name] = nil
		if name == "WordDocument" || name == "0Table" || name == "1Table" {
			streams[name] = stream
		}
	}
	return streams, nil
}

// previewTicketDOC 按 MS-DOC 2.4.1 的 CP/Pcd 对应关系读取主正文，忽略页眉、批注等子文档。
func previewTicketDOC(data []byte) (string, error) {
	streams, err := readTicketDOCStreams(data)
	if err != nil {
		return "", err
	}
	word := streams["WordDocument"]
	le := binary.LittleEndian
	if len(word) < 154 || le.Uint16(word[32:34]) != 14 || le.Uint16(word[62:64]) != 22 {
		return "", fmt.Errorf("DOC 正文索引不完整，请另存为 DOCX")
	}
	cbMac := le.Uint32(word[64:68])
	ccpText := le.Uint32(word[76:80])
	fcCount := int(le.Uint16(word[152:154]))
	if cbMac > uint32(len(word)) || cbMac < 154 || fcCount < 34 || 154+fcCount*8 > int(cbMac) || ccpText > ticketWordPreviewMaxChars {
		return "", fmt.Errorf("DOC 正文索引无效或超过 200 万字符预览上限")
	}
	word = word[:cbMac]
	tableName := "0Table"
	if le.Uint16(word[10:12])&0x0200 != 0 {
		tableName = "1Table"
	}
	table := streams[tableName]
	fcClx := uint64(le.Uint32(word[418:422]))
	lcbClx := uint64(le.Uint32(word[422:426]))
	if lcbClx == 0 || fcClx+lcbClx > uint64(len(table)) {
		return "", fmt.Errorf("DOC 缺少有效正文分段表")
	}
	clx := table[fcClx : fcClx+lcbClx]
	for len(clx) > 0 && clx[0] == 1 {
		if len(clx) < 3 {
			return "", fmt.Errorf("DOC 格式分段损坏")
		}
		size := int(le.Uint16(clx[1:3]))
		if size > 0x3fa2 || size+3 > len(clx) {
			return "", fmt.Errorf("DOC 格式分段越界")
		}
		clx = clx[size+3:]
	}
	if len(clx) < 9 || clx[0] != 2 || uint64(le.Uint32(clx[1:5])) != uint64(len(clx)-5) || (len(clx)-9)%12 != 0 {
		return "", fmt.Errorf("DOC 正文分段结构无效")
	}
	plc := clx[5:]
	count := (len(plc) - 4) / 12
	if count == 0 || le.Uint32(plc[:4]) != 0 || le.Uint32(plc[count*4:count*4+4]) < ccpText {
		return "", fmt.Errorf("DOC 正文分段不完整")
	}
	units := make([]uint16, 0, ccpText)
	for i := 0; i < count; i++ {
		start, end := le.Uint32(plc[i*4:i*4+4]), le.Uint32(plc[i*4+4:i*4+8])
		if end <= start {
			return "", fmt.Errorf("DOC 正文分段顺序无效")
		}
		pcd := plc[(count+1)*4+i*8 : (count+1)*4+(i+1)*8]
		fc := le.Uint32(pcd[2:6])
		compressed := fc&0x40000000 != 0
		offset := uint64(fc & 0x3fffffff)
		width := uint64(2)
		if compressed {
			offset /= 2
			width = 1
		}
		if offset+uint64(end-start)*width > uint64(len(word)) {
			return "", fmt.Errorf("DOC 正文指针超出文件范围")
		}
		if start >= ccpText {
			continue
		}
		if end > ccpText {
			end = ccpText
		}
		for cp := start; cp < end; cp++ {
			if compressed {
				units = append(units, ticketDOCCompressedChar(word[offset]))
			} else {
				units = append(units, le.Uint16(word[offset:offset+2]))
			}
			offset += width
		}
	}
	// 代理项可以跨越 piece 边界，所以拼接后再统一检查，不能按分段单独解码。
	for i := 0; i < len(units); i++ {
		if units[i] >= 0xd800 && units[i] <= 0xdbff {
			if i+1 == len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", fmt.Errorf("DOC 正文 Unicode 编码损坏")
			}
			i++
		} else if units[i] >= 0xdc00 && units[i] <= 0xdfff {
			return "", fmt.Errorf("DOC 正文 Unicode 编码损坏")
		}
	}
	return ticketDOCDisplayText(utf16.Decode(units))
}

// MS-DOC 的 FcCompressed 使用单字节 Unicode，并对以下印刷字符规定特殊映射。
func ticketDOCCompressedChar(value byte) uint16 {
	special := [...]uint16{0x80, 0x81, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021, 0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8d, 0x8e, 0x8f, 0x90, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014, 0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0x9d, 0x9e, 0x0178}
	if value >= 0x80 && value <= 0x9f {
		return special[value-0x80]
	}
	return uint16(value)
}

// ticketDOCDisplayText 保留域的静态结果，丢弃指令并拒绝危险动态域；控制标记不带到浏览器。
func ticketDOCDisplayText(text []rune) (string, error) {
	var out, instructions strings.Builder
	var fields []bool
	for _, r := range text {
		switch r {
		case 0x13:
			if len(fields) >= 64 {
				return "", fmt.Errorf("DOC 域嵌套过深")
			}
			fields = append(fields, false)
			instructions.WriteByte(' ')
			continue
		case 0x14:
			if len(fields) == 0 || fields[len(fields)-1] {
				return "", fmt.Errorf("DOC 域结构损坏")
			}
			fields[len(fields)-1] = true
			instructions.WriteByte(' ')
			continue
		case 0x15:
			if len(fields) == 0 {
				return "", fmt.Errorf("DOC 域结构损坏")
			}
			fields = fields[:len(fields)-1]
			instructions.WriteByte(' ')
			continue
		}
		visible := true
		for _, result := range fields {
			visible = visible && result
		}
		if !visible {
			instructions.WriteRune(r)
			continue
		}
		switch r {
		case '\r', '\v', '\f':
			r = '\n'
		case 7:
			r = '\t'
		case '\t', '\n':
		default:
			if unicode.IsControl(r) {
				continue
			}
		}
		out.WriteRune(r)
	}
	if len(fields) != 0 || unsafeTicketWordField(instructions.String()) {
		return "", fmt.Errorf("DOC 含有损坏或危险的动态域")
	}
	return out.String(), nil
}
