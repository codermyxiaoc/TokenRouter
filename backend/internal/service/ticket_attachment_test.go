package service

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func ticketTestImage(t *testing.T, format string) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var err error
	switch format {
	case "png":
		err = png.Encode(&out, img)
	case "jpeg":
		err = jpeg.Encode(&out, img, nil)
	case "gif":
		err = gif.Encode(&out, img, nil)
	}
	require.NoError(t, err)
	return out.Bytes()
}

const ticketTestContentTypes = `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`
const ticketTestDocument = `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>工单附件</w:t></w:r></w:p></w:body></w:document>`

func ticketTestZIP(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for name, data := range entries {
		entry, err := w.Create(name)
		require.NoError(t, err)
		_, err = entry.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return out.Bytes()
}

// ticketTestDOC 构造有真实 FAT、根目录树和 WordDocument 流的受限 CFB 测试文档。
func ticketTestDOC() []byte {
	const sectorSize = 512
	const free uint32 = 0xffffffff
	const end uint32 = 0xfffffffe
	data := make([]byte, 11*sectorSize)
	le := binary.LittleEndian
	copy(data, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1})
	le.PutUint16(data[26:28], 3)
	le.PutUint16(data[28:30], 0xfffe)
	le.PutUint16(data[30:32], 9)
	le.PutUint16(data[32:34], 6)
	le.PutUint32(data[44:48], 1)
	le.PutUint32(data[48:52], 1)
	le.PutUint32(data[56:60], 4096)
	le.PutUint32(data[60:64], end)
	le.PutUint32(data[68:72], end)
	for offset := 76; offset < 512; offset += 4 {
		le.PutUint32(data[offset:offset+4], free)
	}
	le.PutUint32(data[76:80], 0)
	fat := data[512:1024]
	for offset := 0; offset < sectorSize; offset += 4 {
		le.PutUint32(fat[offset:offset+4], free)
	}
	le.PutUint32(fat[0:4], 0xfffffffd)
	le.PutUint32(fat[4:8], end)
	for id := 2; id < 9; id++ {
		le.PutUint32(fat[id*4:(id+1)*4], uint32(id+1))
	}
	le.PutUint32(fat[36:40], end)
	entry := func(offset int, name string, kind byte, start uint32, size uint64, child uint32) {
		block := data[offset : offset+128]
		name16 := utf16.Encode([]rune(name))
		for i, r := range name16 {
			le.PutUint16(block[i*2:i*2+2], r)
		}
		le.PutUint16(block[64:66], uint16((len(name16)+1)*2))
		block[66], block[67] = kind, 1
		le.PutUint32(block[68:72], free)
		le.PutUint32(block[72:76], free)
		le.PutUint32(block[76:80], child)
		le.PutUint32(block[116:120], start)
		le.PutUint64(block[120:128], size)
	}
	entry(1024, "Root Entry", 5, end, 0, 1)
	entry(1152, "WordDocument", 2, 2, 4096, free)
	le.PutUint16(data[1536:1538], 0xa5ec)
	le.PutUint16(data[1538:1540], 0x00c1)
	return data
}

func TestTicketAttachmentValidFormatsAndCanonicalMIME(t *testing.T) {
	webp, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	require.NoError(t, err)
	tests := []struct {
		name, mime string
		data       []byte
	}{
		{"照片.JPG", "image/jpeg", ticketTestImage(t, "jpeg")},
		{"照片.jpeg", "image/jpeg", ticketTestImage(t, "jpeg")},
		{"截图.png", "image/png", ticketTestImage(t, "png")},
		{"动画.gif", "image/gif", ticketTestImage(t, "gif")},
		{"截图.webp", "image/png", webp},
		{"账单.pdf", "application/pdf", ticketTestPDF()},
		{"说明.doc", "application/msword", ticketTestDOC()},
		{"说明.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument)})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: tt.name, ContentType: "text/html", Data: tt.data}}, defaultTicketConfig())
			require.NoError(t, err)
			require.Len(t, files, 1)
			require.Equal(t, tt.mime, files[0].ContentType)
			if !strings.HasPrefix(tt.mime, "image/") {
				require.Equal(t, tt.data, files[0].Data)
			} else {
				_, _, err := image.Decode(bytes.NewReader(files[0].Data))
				require.NoError(t, err)
			}
		})
	}
}

func TestTicketAttachmentRejectsDisguisedContent(t *testing.T) {
	excel := ticketTestDOC()
	for i := 1152; i < 1216; i++ {
		excel[i] = 0
	}
	for i, r := range utf16.Encode([]rune("Workbook")) {
		binary.LittleEndian.PutUint16(excel[1152+i*2:1154+i*2], r)
	}
	binary.LittleEndian.PutUint16(excel[1216:1218], 18)
	docx := ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument)})
	tests := []struct {
		name string
		data []byte
	}{
		{"html.png", []byte("<html>伪装图片</html>")},
		{"png.jpg", ticketTestImage(t, "png")},
		{"pdf.doc", []byte("%PDF-1.7")},
		{"text.pdf", []byte("这是一个PDF")},
		{"excel.doc", excel},
		{"macro.docm", docx},
		{"script.svg", []byte(`<svg onload="alert(1)"/>`)},
		{"file.exe", []byte("MZ executable")},
		{"empty.png", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: tt.name, Data: tt.data}}, defaultTicketConfig())
			require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
		})
	}
}

func TestTicketAttachmentLimits(t *testing.T) {
	cfg := defaultTicketConfig()
	files := make([]TicketAttachmentUpload, 6)
	_, err := ValidateTicketAttachments(files, cfg)
	require.Equal(t, "TICKET_ATTACHMENT_LIMIT", infraerrors.Reason(err))
	cfg.MaxAttachments = 0
	_, err = ValidateTicketAttachments(files[:1], cfg)
	require.Equal(t, "TICKET_ATTACHMENT_LIMIT", infraerrors.Reason(err))
	result, err := ValidateTicketAttachments(nil, cfg)
	require.NoError(t, err)
	require.Empty(t, result)
	cfg.MaxAttachments, cfg.MaxAttachmentSizeMB = 10, 1
	_, err = ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "huge.pdf", Data: make([]byte, (1<<20)+1)}}, cfg)
	require.Equal(t, "TICKET_ATTACHMENT_TOO_LARGE", infraerrors.Reason(err))
	cfg.MaxAttachmentSizeMB = 20
	large := make([]byte, 20<<20)
	files = []TicketAttachmentUpload{{Name: "1.pdf", Data: large}, {Name: "2.pdf", Data: large}, {Name: "3.pdf", Data: large[:10<<20]}}
	_, err = ValidateTicketAttachments(files, cfg)
	// 恰好达到总量上限时继续实际格式校验，不把伪造 PDF 当成安全内容。
	require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
	files[2].Data = large[:(10<<20)+1]
	_, err = ValidateTicketAttachments(files, cfg)
	require.Equal(t, "TICKET_ATTACHMENT_TOO_LARGE", infraerrors.Reason(err))
	_, err = ValidateTicketAttachments(nil, nil)
	require.Error(t, err)
	cfg.MaxAttachments = 100
	_, err = ValidateTicketAttachments(nil, cfg)
	require.Error(t, err)
}

func TestTicketAttachmentRejectsImageDimensionBomb(t *testing.T) {
	data := ticketTestImage(t, "png")
	binary.BigEndian.PutUint32(data[16:20], 20000)
	binary.BigEndian.PutUint32(data[20:24], 20000)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	_, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "huge.png", Data: data}}, defaultTicketConfig())
	require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
	require.Contains(t, infraerrors.Message(err), "图片尺寸过大")
}

func TestTicketAttachmentFilenameCleanup(t *testing.T) {
	tests := []struct{ input, want string }{
		{`C:\fakepath\截图.png`, "截图.png"},
		{`../../secret/账单.pdf`, "账单.pdf"},
		{"../截\r\n图\x00.png", "截图.png"},
		{"报价\u202edoc.exe.pdf", "报价doc.exe.pdf"},
		{`  不合法:<文件>?.docx  `, "不合法文件.docx"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, sanitizeTicketFilename(tt.input))
	}
	name := sanitizeTicketFilename(strings.Repeat("中", 100) + ".png")
	require.LessOrEqual(t, len(name), ticketMaxFilenameBytes)
	require.True(t, utf8.ValidString(name))
	require.True(t, strings.HasSuffix(name, ".png"))
	files, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: `C:\fakepath\截图.png`, Data: ticketTestImage(t, "png")}}, defaultTicketConfig())
	require.NoError(t, err)
	require.Equal(t, "截图.png", files[0].Name)
}

func TestTicketAttachmentDocxStructuralLimits(t *testing.T) {
	tests := []struct {
		name    string
		entries map[string][]byte
	}{
		{"缺少主文档", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes)}},
		{"空ZIP", map[string][]byte{}},
		{"错误主类型", map[string][]byte{"[Content_Types].xml": []byte(strings.ReplaceAll(ticketTestContentTypes, "wordprocessingml.document", "spreadsheetml.sheet")), "word/document.xml": []byte(ticketTestDocument)}},
		{"宏伪装DOCX", map[string][]byte{"[Content_Types].xml": []byte(strings.ReplaceAll(ticketTestContentTypes, "wordprocessingml.document", "ms-word.document.macroEnabled")), "word/document.xml": []byte(ticketTestDocument)}},
		{"隐藏宏", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument), "word/vbaProject.bin": []byte("macro")}},
		{"路径穿越", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument), "../payload": []byte("data")}},
		{"错误XML根", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(`<html/>`)}},
		{"损坏XML", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(`<w:document><broken>`)}},
		{"多个XML根", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument + ticketTestDocument)}},
		{"XML嵌套炸弹", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(strings.Replace(ticketTestDocument, "<w:body>", "<w:body>"+strings.Repeat("<w:p>", 257)+strings.Repeat("</w:p>", 257), 1))}},
		{"编码隐藏宏", map[string][]byte{"[Content_Types].xml": []byte(strings.ReplaceAll(ticketTestContentTypes, "wordprocessingml.document", "ms-word.document.macro&#69;nabled")), "word/document.xml": []byte(ticketTestDocument)}},
		{"XML解压超限", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": bytes.Repeat([]byte("x"), ticketMaxDocxXMLBytes+1)}},
		{"总解压超限", map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument), "word/media/bomb": bytes.Repeat([]byte("x"), ticketMaxDocxExpandedBytes+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "file.docx", Data: ticketTestZIP(t, tt.entries)}}, defaultTicketConfig())
			require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
		})
	}
	entries := make(map[string][]byte, ticketMaxDocxEntries+1)
	for i := 0; i <= ticketMaxDocxEntries; i++ {
		entries[strings.Repeat("a", i+1)] = nil
	}
	require.Error(t, validateTicketDocx(ticketTestZIP(t, entries)))
	require.NoError(t, validateTicketDocx(ticketTestZIP(t, map[string][]byte{"[Content_Types].xml": []byte("\xef\xbb\xbf" + ticketTestContentTypes), "word/document.xml": []byte("\xef\xbb\xbf" + ticketTestDocument)})))
}

func TestTicketAttachmentDOCRejectsCorruptDirectory(t *testing.T) {
	t.Run("目录链循环", func(t *testing.T) {
		data := ticketTestDOC()
		binary.LittleEndian.PutUint32(data[516:520], 1)
		require.Error(t, validateTicketDOC(data))
	})
	t.Run("目录树循环", func(t *testing.T) {
		data := ticketTestDOC()
		binary.LittleEndian.PutUint32(data[1092:1096], 0)
		binary.LittleEndian.PutUint32(data[1100:1104], 0)
		require.Error(t, validateTicketDOC(data))
	})
	t.Run("孤立Word目录", func(t *testing.T) {
		data := ticketTestDOC()
		binary.LittleEndian.PutUint32(data[1100:1104], 0xffffffff)
		require.Error(t, validateTicketDOC(data))
	})
	t.Run("伪造Word流", func(t *testing.T) {
		data := ticketTestDOC()
		copy(data[1536:1540], "xlsx")
		require.Error(t, validateTicketDOC(data))
	})
	t.Run("截断结构", func(t *testing.T) {
		require.Error(t, validateTicketDOC(ticketTestDOC()[:1025]))
	})
}
