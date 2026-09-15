package service

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// ticketPreviewTestDOC 用真实 FAT、FIB 和 piece table 构造静态 Word 文档，覆盖两种编码混排。
func ticketPreviewTestDOC(compressed []byte, text string, miniTable bool) []byte {
	le := binary.LittleEndian
	data := append(ticketTestDOC(), make([]byte, 8*512)...)
	entry := data[1280:1408]
	for i, unit := range utf16.Encode([]rune("1Table")) {
		le.PutUint16(entry[i*2:i*2+2], unit)
	}
	le.PutUint16(entry[64:66], 14)
	entry[66], entry[67] = 2, 1
	le.PutUint32(entry[68:72], ticketDOCFree)
	le.PutUint32(entry[72:76], ticketDOCFree)
	le.PutUint32(entry[76:80], ticketDOCFree)
	le.PutUint32(entry[116:120], 10)
	le.PutUint64(entry[120:128], 4096)
	le.PutUint32(data[1152+72:1152+76], 2)
	for id := 10; id < 17; id++ {
		le.PutUint32(data[512+id*4:512+id*4+4], uint32(id+1))
	}
	le.PutUint32(data[512+17*4:512+17*4+4], ticketDOCEnd)
	word := data[1536 : 1536+4096]
	le.PutUint16(word[10:12], 0x0200)
	le.PutUint16(word[32:34], 14)
	le.PutUint16(word[62:64], 22)
	le.PutUint16(word[152:154], 93)
	le.PutUint32(word[64:68], 4096)
	units := utf16.Encode([]rune(text))
	le.PutUint32(word[76:80], uint32(len(compressed)+len(units)))
	// 物理存储顺序刻意与正文顺序相反，防止预览退化为扫描二进制字符串。
	copy(word[1400:], compressed)
	for i, unit := range units {
		le.PutUint16(word[1024+i*2:1026+i*2], unit)
	}
	table := data[5632:]
	table[0] = 2
	le.PutUint32(table[1:5], 28)
	le.PutUint32(table[5:9], 0)
	le.PutUint32(table[9:13], uint32(len(compressed)))
	le.PutUint32(table[13:17], uint32(len(compressed)+len(units)))
	le.PutUint32(table[19:23], 0x40000000|2800)
	le.PutUint32(table[27:31], 1024)
	le.PutUint32(word[418:422], 0)
	le.PutUint32(word[422:426], 33)
	if miniTable {
		le.PutUint32(entry[116:120], 0)
		le.PutUint64(entry[120:128], 33)
		le.PutUint32(data[1024+116:1024+120], 10)
		le.PutUint64(data[1024+120:1024+128], 64)
		le.PutUint32(data[60:64], 11)
		le.PutUint32(data[64:68], 1)
		le.PutUint32(data[512+10*4:512+10*4+4], ticketDOCEnd)
		le.PutUint32(data[512+11*4:512+11*4+4], ticketDOCEnd)
		for offset := 6144; offset < 6656; offset += 4 {
			le.PutUint32(data[offset:offset+4], ticketDOCFree)
		}
		le.PutUint32(data[6144:6148], ticketDOCEnd)
		data = data[:6656]
	}
	return data
}

func TestPreviewTicketWordDOCXTextAndTables(t *testing.T) {
	body := `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>中文 Hello &lt;script&gt;</w:t><w:tab/><w:t>第二段</w:t><w:br/><w:t>换行</w:t></w:r></w:p><w:tbl><w:tr><w:tc><w:p><w:r><w:t>项目</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Price 10</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`
	data := ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(body)})
	text, err := PreviewTicketWord(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	require.NoError(t, err)
	require.Equal(t, "中文 Hello <script>\t第二段\n换行\n项目 \tPrice 10", text)
	// 严格版 OOXML 命名空间与默认 Transitional 命名空间使用相同的正文提取规则。
	body = strings.ReplaceAll(body, "http://schemas.openxmlformats.org/wordprocessingml/2006/main", "http://purl.oclc.org/ooxml/wordprocessingml/main")
	data = ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(body)})
	text, err = PreviewTicketWord(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	require.NoError(t, err)
	require.Contains(t, text, "中文 Hello <script>")
}

func TestPreviewTicketWordDOCMixedText(t *testing.T) {
	for _, mini := range []bool{false, true} {
		data := ticketPreviewTestDOC([]byte("English \x93quoted\x94\r"), "中文工单 😀\r表格\x07第二格\r", mini)
		text, err := PreviewTicketWord(data, "application/msword")
		require.NoError(t, err)
		require.Equal(t, "English “quoted”\n中文工单 😀\n表格\t第二格", text)
	}
}

func TestPreviewTicketWordDOCMainTextOnly(t *testing.T) {
	data := ticketPreviewTestDOC([]byte("Main body\r"), "隐藏批注", false)
	binary.LittleEndian.PutUint32(data[1536+76:1536+80], uint32(len("Main body\r")))
	text, err := PreviewTicketWord(data, "application/msword")
	require.NoError(t, err)
	require.Equal(t, "Main body", text)
}

func TestPreviewTicketWordDOCMiniStreamMainDocument(t *testing.T) {
	le := binary.LittleEndian
	original := ticketPreviewTestDOC([]byte("Mini English\r"), "中文小流\r", true)
	data := append(original, make([]byte, 2048)...)
	// 小主文档与表流共享 root mini stream，但各自占有不重叠的 mini sector 链。
	word := append([]byte(nil), original[1536:1536+2048]...)
	le.PutUint32(word[64:68], 2048)
	copy(data[5632+64:5632+64+2048], word)
	le.PutUint32(data[1152+116:1152+120], 1)
	le.PutUint64(data[1152+120:1152+128], 2048)
	le.PutUint64(data[1024+120:1024+128], 64+2048)
	le.PutUint32(data[60:64], 15)
	for id := 10; id < 14; id++ {
		le.PutUint32(data[512+id*4:512+id*4+4], uint32(id+1))
	}
	le.PutUint32(data[512+14*4:512+14*4+4], ticketDOCEnd)
	le.PutUint32(data[512+15*4:512+15*4+4], ticketDOCEnd)
	mini := data[8192:8704]
	for offset := 0; offset < 512; offset += 4 {
		le.PutUint32(mini[offset:offset+4], ticketDOCFree)
	}
	le.PutUint32(mini[:4], ticketDOCEnd)
	for id := 1; id < 32; id++ {
		le.PutUint32(mini[id*4:id*4+4], uint32(id+1))
	}
	le.PutUint32(mini[128:132], ticketDOCEnd)
	text, err := PreviewTicketWord(data, "application/msword")
	require.NoError(t, err)
	require.Equal(t, "Mini English\n中文小流", text)
	// 主文档小流中途形成循环也必须报错，不能输出已读前缀冒充完整内容。
	le.PutUint32(mini[8:12], 1)
	text, err = PreviewTicketWord(data, "application/msword")
	require.Error(t, err)
	require.Empty(t, text)
}

func TestPreviewTicketWordDOCUnicodeSurrogateAcrossPieces(t *testing.T) {
	le := binary.LittleEndian
	data := ticketPreviewTestDOC([]byte("X"), "X", false)
	le.PutUint32(data[5632+19:5632+23], 1024)
	le.PutUint32(data[5632+27:5632+31], 1026)
	le.PutUint16(data[1536+1024:1536+1026], 0xd83d)
	le.PutUint16(data[1536+1026:1536+1028], 0xde00)
	text, err := PreviewTicketWord(data, "application/msword")
	require.NoError(t, err)
	require.Equal(t, "😀", text)
}

func TestPreviewTicketWordRevalidatesHistoricalDangerousContent(t *testing.T) {
	tests := map[string]string{
		"宏":   `<w:p><w:object/></w:p>`,
		"动态域": `<w:p><w:r><w:instrText>DDEAUTO</w:instrText></w:r></w:p>`,
		"实体":  `<!DOCTYPE data [<!ENTITY x SYSTEM "file:///etc/passwd">]>`,
	}
	for name, fragment := range tests {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(ticketTestDocument, "<w:body>", "<w:body>"+fragment, 1)
			data := ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(body)})
			text, err := PreviewTicketWord(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
			require.Error(t, err)
			require.Empty(t, text)
		})
	}
	data := ticketPreviewTestDOC([]byte("Text\r"), "\x13 DDEAUTO cmd malicious \x14静态结果\x15", false)
	text, err := PreviewTicketWord(data, "application/msword")
	require.Error(t, err)
	require.Empty(t, text)
	data = ticketPreviewTestDOC([]byte("Text\r"), "\x13 HYPERLINK https://example.com \x14链接正文\x15", false)
	text, err = PreviewTicketWord(data, "application/msword")
	require.NoError(t, err)
	require.Equal(t, "Text\n链接正文", text)
}

func TestPreviewTicketWordDOCCorruptAndResourceLimits(t *testing.T) {
	le := binary.LittleEndian
	tests := map[string]func([]byte){
		"正文指针越界":  func(data []byte) { le.PutUint32(data[5632+19:5632+23], 0x7ffffffe) },
		"分段计数越界":  func(data []byte) { le.PutUint32(data[5632+1:5632+5], 0xffffffff) },
		"分段顺序无效":  func(data []byte) { le.PutUint32(data[5632+9:5632+13], 0) },
		"正文长度超限":  func(data []byte) { le.PutUint32(data[1536+76:1536+80], ticketWordPreviewMaxChars+1) },
		"格式表越界":   func(data []byte) { le.PutUint16(data[1536+152:1536+154], 0xffff) },
		"表流链循环":   func(data []byte) { le.PutUint32(data[512+10*4:512+10*4+4], 10) },
		"表流与正文重叠": func(data []byte) { le.PutUint32(data[1280+116:1280+120], 2) },
		"正文无效代理项": func(data []byte) { le.PutUint16(data[1536+1024:1536+1026], 0xd800) },
		"加密DOC":   func(data []byte) { le.PutUint16(data[1536+10:1536+12], 0x0300) },
		"嵌入存储":    func(data []byte) { data[1408+66] = 1 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			data := ticketPreviewTestDOC([]byte("Text\r"), "中文工单\r", false)
			mutate(data)
			text, err := PreviewTicketWord(data, "application/msword")
			require.Error(t, err)
			require.Empty(t, text)
		})
	}
	data := ticketPreviewTestDOC([]byte("Text\r"), "正文", true)
	le.PutUint32(data[6144:6148], 0)
	_, err := PreviewTicketWord(data, "application/msword")
	require.Error(t, err)
}

func TestPreviewTicketWordLimitsAndUnsupportedType(t *testing.T) {
	_, err := PreviewTicketWord(make([]byte, ticketWordPreviewMaxInput+1), "application/msword")
	require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
	_, err = PreviewTicketWord([]byte("text"), "text/html")
	require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
	body := strings.Replace(ticketTestDocument, "工单附件", strings.Repeat("a", ticketWordPreviewMaxBytes+1), 1)
	data := ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(body)})
	_, err = PreviewTicketWord(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	require.Equal(t, "TICKET_ATTACHMENT_PREVIEW_UNAVAILABLE", infraerrors.Reason(err))
	ticketWordPreviewSlots <- struct{}{}
	ticketWordPreviewSlots <- struct{}{}
	defer func() { <-ticketWordPreviewSlots; <-ticketWordPreviewSlots }()
	_, err = PreviewTicketWord(ticketTestDOC(), "application/msword")
	require.ErrorIs(t, err, ErrTicketAttachmentBusy)
}
