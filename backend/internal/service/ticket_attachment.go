package service

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	_ "golang.org/x/image/webp"
)

// TicketMaxTotalAttachmentBytes 是每次创建或回复的附件总量硬上限，HTTP 层也必须限制请求体。
const TicketMaxTotalAttachmentBytes = 50 << 20

// ErrTicketAttachmentBusy 区分暂时的处理容量不足与文件本身不安全，客户端可以保留附件后重试。
var ErrTicketAttachmentBusy = infraerrors.ServiceUnavailable("TICKET_ATTACHMENT_BUSY", "附件安全检查繁忙，请稍后重试")

const (
	ticketMaxImageDimension    = 16384
	ticketMaxImagePixels       = 40_000_000
	ticketMaxDocxEntries       = 1024
	ticketMaxDocxExpandedBytes = 64 << 20
	ticketMaxDocxXMLBytes      = 16 << 20
	ticketMaxFilenameBytes     = 180
)

// ValidateTicketAttachments 校验实际内容并返回规范文件名及可信 MIME，不能仅依赖浏览器声明。
// @project-doc docs/domains/support_tickets.md#ticket_attachments
func ValidateTicketAttachments(files []TicketAttachmentUpload, cfg *TicketConfig) ([]TicketAttachmentUpload, error) {
	if cfg == nil || cfg.MaxAttachments < 0 || cfg.MaxAttachments > 10 || cfg.MaxAttachmentSizeMB < 1 || cfg.MaxAttachmentSizeMB > 20 {
		return nil, fmt.Errorf("工单附件配置无效")
	}
	if len(files) > cfg.MaxAttachments || len(files) > 10 {
		return nil, infraerrors.BadRequest("TICKET_ATTACHMENT_LIMIT", fmt.Sprintf("每次最多上传 %d 个附件", cfg.MaxAttachments))
	}
	total := int64(0)
	for _, file := range files {
		size := int64(len(file.Data))
		if size == 0 {
			return nil, ticketAttachmentInvalid("附件不能为空")
		}
		if size > int64(cfg.MaxAttachmentSizeMB)<<20 {
			return nil, infraerrors.BadRequest("TICKET_ATTACHMENT_TOO_LARGE", fmt.Sprintf("单个附件不能超过 %d MiB", cfg.MaxAttachmentSizeMB))
		}
		total += size
		if total > TicketMaxTotalAttachmentBytes {
			return nil, infraerrors.BadRequest("TICKET_ATTACHMENT_TOO_LARGE", "每次上传的附件总大小不能超过 50 MiB")
		}
	}
	out := make([]TicketAttachmentUpload, 0, len(files))
	total = 0
	for _, file := range files {
		name := sanitizeTicketFilename(file.Name)
		if err := validateTicketFilename(name); err != nil {
			return nil, ticketAttachmentInvalid(err.Error())
		}
		ext := strings.ToLower(path.Ext(name))
		mime, err := ticketAttachmentMIME(file.Data, ext)
		if err != nil {
			if errors.Is(err, ErrTicketAttachmentBusy) {
				return nil, ErrTicketAttachmentBusy
			}
			return nil, ticketAttachmentInvalid(fmt.Sprintf("附件 %q 无效：%s", name, err.Error()))
		}
		data := file.Data
		if strings.HasPrefix(mime, "image/") {
			data, mime, err = normalizeTicketImage(data, ext, int64(cfg.MaxAttachmentSizeMB)<<20)
			if err != nil {
				if errors.Is(err, ErrTicketAttachmentBusy) {
					return nil, ErrTicketAttachmentBusy
				}
				return nil, ticketAttachmentInvalid(fmt.Sprintf("附件 %q 无效：%s", name, err.Error()))
			}
			if ext == ".webp" {
				name = strings.TrimSuffix(name, path.Ext(name)) + ".png"
			}
		}
		total += int64(len(data))
		if total > TicketMaxTotalAttachmentBytes {
			return nil, infraerrors.BadRequest("TICKET_ATTACHMENT_TOO_LARGE", "安全处理后的附件总大小不能超过 50 MiB")
		}
		out = append(out, TicketAttachmentUpload{Name: name, ContentType: mime, Data: data})
	}
	return out, nil
}

// validateTicketFilename 拒绝可误导下游系统的危险双后缀；文件名从不用于本地执行或存储路径。
func validateTicketFilename(name string) error {
	for _, part := range strings.Split(strings.ToLower(name), ".") {
		switch part {
		case "php", "php3", "php4", "php5", "phtml", "asp", "aspx", "jsp", "jspx", "js", "mjs", "html", "htm", "svg", "exe", "dll", "com", "bat", "cmd", "ps1", "vbs", "sh", "py", "jar", "msi", "scr", "hta", "docm":
			return fmt.Errorf("附件名称不能包含脚本或可执行文件后缀")
		}
	}
	return nil
}

// 同时最多处理两张图片，限制完整解码的峰值内存；不积压等待中的大文件。
var ticketImageSlots = make(chan struct{}, 2)

type ticketImageBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *ticketImageBuffer) Write(p []byte) (int, error) {
	if int64(b.Len())+int64(len(p)) > b.limit {
		return 0, fmt.Errorf("安全处理后的图片超过单文件大小限制")
	}
	return b.Buffer.Write(p)
}

// normalizeTicketImage 仅保留解码后的像素，去掉元数据和尾随载荷；GIF 保留首帧，WebP 转为 PNG。
func normalizeTicketImage(data []byte, ext string, limit int64) ([]byte, string, error) {
	select {
	case ticketImageSlots <- struct{}{}:
		defer func() { <-ticketImageSlots }()
	default:
		return nil, "", ErrTicketAttachmentBusy
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("图片内容损坏或不完整")
	}
	var output ticketImageBuffer
	output.limit = limit
	switch format {
	case "jpeg":
		err = jpeg.Encode(&output, img, &jpeg.Options{Quality: 95})
	case "gif":
		err = gif.Encode(&output, img, nil)
	default:
		format = "png"
		err = png.Encode(&output, img)
	}
	if err != nil {
		return nil, "", err
	}
	return output.Bytes(), "image/" + format, nil
}

func ticketAttachmentInvalid(message string) error {
	return infraerrors.BadRequest("TICKET_ATTACHMENT_INVALID", message)
}

// sanitizeTicketFilename 同时清理 Windows 和 Unix 路径及控制字符，限制 UTF-8 字节数并保留扩展名。
func sanitizeTicketFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`<>:"|?*`, r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		return "attachment"
	}
	if len(name) <= ticketMaxFilenameBytes {
		return name
	}
	ext := path.Ext(name)
	if len(ext) > 10 {
		ext = ""
	}
	base := strings.TrimSuffix(name, ext)
	for len(base)+len(ext) > ticketMaxFilenameBytes {
		_, size := utf8.DecodeLastRuneInString(base)
		base = base[:len(base)-size]
	}
	return base + ext
}

func ticketAttachmentMIME(data []byte, ext string) (string, error) {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return "", fmt.Errorf("无法识别图片内容")
		}
		expected := strings.TrimPrefix(ext, ".")
		if expected == "jpg" {
			expected = "jpeg"
		}
		if format != expected {
			return "", fmt.Errorf("图片内容与扩展名不一致")
		}
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > ticketMaxImageDimension || cfg.Height > ticketMaxImageDimension || int64(cfg.Width)*int64(cfg.Height) > ticketMaxImagePixels {
			return "", fmt.Errorf("图片尺寸过大，边长最多 16384 像素且总像素最多 4000 万")
		}
		return "image/" + format, nil
	case ".pdf":
		if err := validateTicketPDF(data); err != nil {
			return "", err
		}
		return "application/pdf", nil
	case ".docx":
		if err := validateTicketDocx(data); err != nil {
			return "", err
		}
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
	case ".doc":
		if err := validateTicketDOC(data); err != nil {
			return "", err
		}
		return "application/msword", nil
	default:
		return "", fmt.Errorf("仅支持 JPG、PNG、GIF、WebP、PDF、DOC 和 DOCX 文件")
	}
}

// validateTicketDocx 只读取受限 ZIP/XML 数据，不解压落盘、不解析嵌入对象或执行宏。
func validateTicketDocx(data []byte) error {
	// ZIP 必须从文件开头开始且结束目录位于末尾，不能用可执行前缀或尾随载荷伪装。
	endOffset := bytes.LastIndex(data, []byte{'P', 'K', 5, 6})
	if !bytes.HasPrefix(data, []byte{'P', 'K', 3, 4}) || endOffset < 0 || len(data)-endOffset < 22 || endOffset+22+int(binary.LittleEndian.Uint16(data[endOffset+20:endOffset+22])) != len(data) {
		return fmt.Errorf("Word 文件 ZIP 边界无效")
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(r.File) > ticketMaxDocxEntries {
		return fmt.Errorf("Word 文件结构无效或包含过多条目")
	}
	seen := make(map[string]bool, len(r.File))
	expanded := int64(0)
	var contentTypes, document []byte
	for _, entry := range r.File {
		name := entry.Name
		lower := strings.ToLower(name)
		if strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.Contains(lower, "vbaproject") || strings.Contains(lower, "activex") || strings.Contains(lower, "embeddings/") || entry.Flags&1 != 0 || seen[lower] || !entry.Mode().IsRegular() && !entry.FileInfo().IsDir() {
			return fmt.Errorf("Word 文件包含无效路径、加密或宏内容")
		}
		seen[lower] = true
		if entry.FileInfo().IsDir() {
			continue
		}
		ext := strings.ToLower(path.Ext(name))
		switch ext {
		case ".xml", ".rels", ".jpg", ".jpeg", ".png", ".gif", ".webp", ".emf", ".wmf", ".tif", ".tiff", ".bmp", ".odttf":
		default:
			return fmt.Errorf("Word 文件包含不支持的嵌入文件")
		}
		if entry.UncompressedSize64 > uint64(ticketMaxDocxExpandedBytes-expanded) {
			return fmt.Errorf("Word 文件解压大小超过 64 MiB")
		}
		rc, openErr := entry.Open()
		if openErr != nil {
			return fmt.Errorf("Word 文件条目无法读取")
		}
		limit := int64(ticketMaxDocxExpandedBytes) - expanded
		var size int64
		var readErr error
		if ext == ".xml" || ext == ".rels" {
			if limit > ticketMaxDocxXMLBytes {
				limit = ticketMaxDocxXMLBytes
			}
			var body []byte
			body, readErr = io.ReadAll(io.LimitReader(rc, limit+1))
			size = int64(len(body))
			if name == "[Content_Types].xml" {
				contentTypes = body
			} else if name == "word/document.xml" {
				document = body
			}
			if readErr == nil && size <= limit {
				readErr = validateTicketOfficeXML(body, ext == ".rels")
			}
		} else {
			size, readErr = io.Copy(io.Discard, io.LimitReader(rc, limit+1))
		}
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil || size > limit {
			return fmt.Errorf("Word 文件条目损坏或解压大小超限")
		}
		expanded += size
	}
	if len(contentTypes) == 0 || len(document) == 0 {
		return fmt.Errorf("Word 文件缺少文档结构")
	}
	wordType := false
	err = validateTicketDocxXML(contentTypes, func(start xml.StartElement, depth int) error {
		if depth == 1 && (start.Name.Local != "Types" || start.Name.Space != "http://schemas.openxmlformats.org/package/2006/content-types") {
			return fmt.Errorf("Word 文档类型无效")
		}
		var partName, contentType string
		for _, attr := range start.Attr {
			if attr.Name.Local == "PartName" {
				partName = attr.Value
			}
			if attr.Name.Local == "ContentType" {
				contentType = attr.Value
			}
		}
		if unsafeTicketOfficeType(contentType) {
			return fmt.Errorf("Word 文档不能包含宏")
		}
		if depth == 2 && start.Name.Local == "Override" && partName == "/word/document.xml" && contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml" {
			wordType = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !wordType {
		return fmt.Errorf("文件不是 DOCX 文档")
	}
	return validateTicketDocxXML(document, func(start xml.StartElement, depth int) error {
		if depth == 1 && (start.Name.Local != "document" || (start.Name.Space != "http://schemas.openxmlformats.org/wordprocessingml/2006/main" && start.Name.Space != "http://purl.oclc.org/ooxml/wordprocessingml/main")) {
			return fmt.Errorf("文件不是 Word 文档")
		}
		return nil
	})
}

func unsafeTicketOfficeType(value string) bool {
	lower := strings.ToLower(value)
	for _, token := range []string{"macroenabled", "vbaproject", "oleobject", "activex", "application/vnd.openxmlformats-officedocument.package"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

// validateTicketOfficeXML 覆盖每个 XML 和关系文件，拒绝宏、对象、动态域和外部资源加载。
func validateTicketOfficeXML(data []byte, relationships bool) error {
	if err := validateTicketDocxXML(data, func(start xml.StartElement, depth int) error {
		name := strings.ToLower(start.Name.Local)
		switch name {
		case "object", "oleobject", "altchunk", "control":
			return fmt.Errorf("Word 文件不能包含嵌入对象或动态内容")
		}
		var relType, target, mode string
		for _, attr := range start.Attr {
			switch strings.ToLower(attr.Name.Local) {
			case "type":
				relType = attr.Value
			case "target":
				target = attr.Value
			case "targetmode":
				mode = attr.Value
			case "contenttype":
				if unsafeTicketOfficeType(attr.Value) {
					return fmt.Errorf("Word 文件不能包含宏或可执行对象")
				}
			case "instr":
				if unsafeTicketWordField(attr.Value) {
					return fmt.Errorf("Word 文件不能包含动态外部域")
				}
			}
		}
		if relationships && name == "relationship" {
			if unsafeTicketOfficeType(relType) || strings.HasSuffix(strings.ToLower(relType), "/attachedtemplate") || strings.HasSuffix(strings.ToLower(relType), "/package") || strings.HasSuffix(strings.ToLower(relType), "/afchunk") {
				return fmt.Errorf("Word 文件不能包含动态关系或嵌入对象")
			}
			if strings.EqualFold(mode, "External") {
				u, err := url.Parse(target)
				if !strings.HasSuffix(strings.ToLower(relType), "/hyperlink") || err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
					return fmt.Errorf("Word 文件不能加载外部资源")
				}
			} else {
				decoded, err := url.PathUnescape(target)
				if err != nil || strings.ContainsAny(decoded, "\\:\x00") || strings.HasPrefix(decoded, "//") {
					return fmt.Errorf("Word 文件包含不安全关系地址")
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// 域指令可能分散在多个 instrText 节点；拼接后检查，不能靠原始 XML 字符串匹配。
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var fields strings.Builder
	inField := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("Word 文档 XML 无效")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if strings.EqualFold(value.Name.Local, "instrText") {
				inField = true
			}
		case xml.EndElement:
			if strings.EqualFold(value.Name.Local, "instrText") {
				inField = false
			}
		case xml.CharData:
			if inField {
				fields.Write(value)
			}
		}
	}
	if unsafeTicketWordField(fields.String()) {
		return fmt.Errorf("Word 文件不能包含动态外部域")
	}
	return nil
}

func unsafeTicketWordField(value string) bool {
	upper := strings.ToUpper(value)
	for _, command := range strings.Fields(upper) {
		switch command {
		case "DDE", "DDEAUTO", "INCLUDETEXT", "INCLUDEPICTURE", "LINK", "DATABASE":
			return true
		}
	}
	for _, token := range []string{"JAVASCRIPT:", "FILE:", `\\`, "MS-WORD:"} {
		if strings.Contains(upper, token) {
			return true
		}
	}
	return false
}

// validateTicketDocxXML 限制 XML 嵌套深度并要求唯一根节点，避免小体积深层 XML 消耗无界内存。
func validateTicketDocxXML(data []byte, visit func(xml.StartElement, int) error) error {
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	depth := 0
	rootFound := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("Word 文档 XML 无效")
		}
		switch value := token.(type) {
		case xml.Directive:
			return fmt.Errorf("Word 文档不能包含 DTD 或实体声明")
		case xml.ProcInst:
			if value.Target != "xml" {
				return fmt.Errorf("Word 文档不能包含处理指令")
			}
		case xml.StartElement:
			if depth == 0 && rootFound {
				return fmt.Errorf("Word 文档 XML 存在多个根节点")
			}
			depth++
			if depth > 256 {
				return fmt.Errorf("Word 文档 XML 嵌套过深")
			}
			rootFound = true
			if err := visit(value, depth); err != nil {
				return err
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(value)) > 0 {
				return fmt.Errorf("Word 文档 XML 存在无效内容")
			}
		}
	}
	if !rootFound || depth != 0 {
		return fmt.Errorf("Word 文档 XML 结构不完整")
	}
	return nil
}

// validateTicketDOC 沿 CFB 的真实 FAT 目录链识别 WordDocument 流，拒绝改名后的 Excel 等 OLE 文件。
func validateTicketDOC(data []byte) error {
	invalid := fmt.Errorf("DOC 文件结构无效，必须是 Word 二进制文档")
	if len(data) < 512 || !bytes.Equal(data[:8], []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
		return invalid
	}
	le := binary.LittleEndian
	version, shift := le.Uint16(data[26:28]), le.Uint16(data[30:32])
	if le.Uint16(data[28:30]) != 0xfffe || le.Uint16(data[32:34]) != 6 || le.Uint32(data[56:60]) != 4096 || !((version == 3 && shift == 9) || (version == 4 && shift == 12)) {
		return invalid
	}
	sectorSize := 1 << shift
	sectorCount := len(data)/sectorSize - 1
	if sectorCount < 2 || len(data)%sectorSize != 0 {
		return invalid
	}
	sector := func(id uint32) []byte {
		if uint64(id) >= uint64(sectorCount) {
			return nil
		}
		start := (int(id) + 1) * sectorSize
		return data[start : start+sectorSize]
	}
	const end uint32 = 0xfffffffe
	const free uint32 = 0xffffffff
	fatCount := le.Uint32(data[44:48])
	if fatCount == 0 || uint64(fatCount) > uint64(sectorCount) {
		return invalid
	}
	fatIDs := make([]uint32, 0, fatCount)
	for offset := 76; offset < 512; offset += 4 {
		id := le.Uint32(data[offset : offset+4])
		if id != free {
			fatIDs = append(fatIDs, id)
		}
	}
	difatID, difatCount := le.Uint32(data[68:72]), le.Uint32(data[72:76])
	if uint64(difatCount) > uint64(sectorCount) {
		return invalid
	}
	visited := make(map[uint32]bool)
	for i := uint32(0); i < difatCount; i++ {
		block := sector(difatID)
		if block == nil || visited[difatID] {
			return invalid
		}
		visited[difatID] = true
		for offset := 0; offset < sectorSize-4; offset += 4 {
			id := le.Uint32(block[offset : offset+4])
			if id != free {
				fatIDs = append(fatIDs, id)
			}
		}
		difatID = le.Uint32(block[sectorSize-4:])
	}
	if len(fatIDs) != int(fatCount) {
		return invalid
	}
	fat := make([]uint32, 0, len(fatIDs)*sectorSize/4)
	for _, id := range fatIDs {
		block := sector(id)
		if block == nil || visited[id] {
			return invalid
		}
		visited[id] = true
		for offset := 0; offset < sectorSize; offset += 4 {
			fat = append(fat, le.Uint32(block[offset:offset+4]))
		}
	}
	var directory []byte
	visited = make(map[uint32]bool)
	for id := le.Uint32(data[48:52]); id != end; {
		block := sector(id)
		if block == nil || visited[id] || int(id) >= len(fat) || len(directory) >= 2<<20 {
			return invalid
		}
		visited[id] = true
		directory = append(directory, block...)
		id = fat[id]
	}
	if len(directory) < 128 || directory[66] != 5 {
		return invalid
	}
	// 宏、ActiveX 和 OLE 嵌入对象依赖子存储，静态 Word 附件不允许任何子存储。
	// 必须检查全部目录，不能发现 WordDocument 后提前返回，遗漏同层恶意条目。
	for offset := 128; offset+128 <= len(directory); offset += 128 {
		entry := directory[offset : offset+128]
		if entry[66] == 1 {
			return fmt.Errorf("DOC 文件不能包含宏、ActiveX 或嵌入对象，请另存为无宏 DOCX")
		}
		if entry[66] != 0 && entry[66] != 2 {
			return invalid
		}
		if entry[66] == 2 {
			nameLength := int(le.Uint16(entry[64:66]))
			if nameLength < 2 || nameLength > 64 || nameLength%2 != 0 {
				return invalid
			}
			var name []uint16
			for i := 0; i < nameLength-2; i += 2 {
				name = append(name, le.Uint16(entry[i:i+2]))
			}
			lower := strings.ToLower(string(utf16.Decode(name)))
			if strings.Contains(lower, "vba") || strings.Contains(lower, "macro") || strings.Contains(lower, "ole10native") || strings.Contains(lower, "package") || lower == "project" || lower == "projectwm" {
				return fmt.Errorf("DOC 文件不能包含宏或嵌入对象")
			}
		}
	}
	// 只查根存储树可达的流，孤立目录中的同名字符串不能冒充 Word 文档。
	directorySectors := visited
	queue := []uint32{le.Uint32(directory[76:80])}
	visited = make(map[uint32]bool)
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if id == free {
			continue
		}
		if uint64(id) >= uint64(len(directory)/128) || visited[id] {
			return invalid
		}
		visited[id] = true
		entry := directory[int(id)*128 : (int(id)+1)*128]
		queue = append(queue, le.Uint32(entry[68:72]), le.Uint32(entry[72:76]))
		nameLength := int(le.Uint16(entry[64:66]))
		if nameLength < 2 || nameLength > 64 || nameLength%2 != 0 || le.Uint16(entry[nameLength-2:nameLength]) != 0 {
			return invalid
		}
		name := make([]uint16, 0, nameLength/2-1)
		for i := 0; i < nameLength-2; i += 2 {
			name = append(name, le.Uint16(entry[i:i+2]))
		}
		if entry[66] == 2 && string(utf16.Decode(name)) == "WordDocument" && le.Uint64(entry[120:128]) >= 2 {
			streamID := le.Uint32(entry[116:120])
			streamSize := le.Uint64(entry[120:128])
			if streamSize > uint64(len(data)) {
				return invalid
			}
			var first []byte
			if streamSize >= 4096 {
				first = sector(streamID)
				remaining := streamSize
				chain := make(map[uint32]bool)
				for current := streamID; ; current = fat[current] {
					if sector(current) == nil || int(current) >= len(fat) || chain[current] || directorySectors[current] {
						return invalid
					}
					chain[current] = true
					if remaining <= uint64(sectorSize) {
						if fat[current] != end {
							return invalid
						}
						break
					}
					remaining -= uint64(sectorSize)
				}
			} else {
				// 小流的首块位于根 mini stream，按 FAT 链定位且不展开其他内容。
				offset := uint64(streamID) * 64
				rootSize := le.Uint64(directory[120:128])
				if offset+2 > rootSize {
					return invalid
				}
				rootID := le.Uint32(directory[116:120])
				rootVisited := make(map[uint32]bool)
				for offset >= uint64(sectorSize) {
					if sector(rootID) == nil || int(rootID) >= len(fat) || rootVisited[rootID] {
						return invalid
					}
					rootVisited[rootID] = true
					rootID = fat[rootID]
					offset -= uint64(sectorSize)
				}
				if block := sector(rootID); block != nil {
					first = block[offset:]
				}
			}
			if len(first) >= 32 && le.Uint16(first[:2]) == 0xa5ec {
				// Word 97 以前的宏布局不在此静态附件支持范围；加密内容无法审查。
				if le.Uint16(first[2:4]) < 0x00c1 || le.Uint16(first[2:4]) > 0x0112 || le.Uint16(first[10:12])&0x8101 != 0 {
					return fmt.Errorf("不支持加密、模板或旧版 DOC，请另存为无宏 DOCX")
				}
				return nil
			}
			return invalid
		}
	}
	return invalid
}
