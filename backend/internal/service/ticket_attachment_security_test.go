package service

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"strings"
	"testing"
	"unicode/utf16"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestTicketAttachmentImageRewritingRemovesPayloadAndRejectsTruncation(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		t.Run(format, func(t *testing.T) {
			original := ticketTestImage(t, format)
			payload := append(append([]byte(nil), original...), []byte("<?php invalid_fixture(); ?><script>inert-test</script>")...)
			files, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "image." + format, Data: payload}}, defaultTicketConfig())
			require.NoError(t, err)
			require.NotContains(t, string(files[0].Data), "inert-test")
			require.NotContains(t, string(files[0].Data), "<?php")
			again, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "image." + format, Data: payload}}, defaultTicketConfig())
			require.NoError(t, err)
			require.Equal(t, files, again, "相同原始上传重试必须产生相同幂等摘要")
			_, err = ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "image." + format, Data: original[:len(original)/2]}}, defaultTicketConfig())
			require.Error(t, err)
		})
	}
}

func TestTicketAttachmentAnimatedGIFKeepsOnlyFirstFrame(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	img := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	var out bytes.Buffer
	require.NoError(t, gif.EncodeAll(&out, &gif.GIF{Image: []*image.Paletted{img, img}, Delay: []int{1, 1}}))
	files, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "animation.gif", Data: out.Bytes()}}, defaultTicketConfig())
	require.NoError(t, err)
	decoded, err := gif.DecodeAll(bytes.NewReader(files[0].Data))
	require.NoError(t, err)
	require.Len(t, decoded.Image, 1)
}

func TestTicketAttachmentDangerousDoubleExtensions(t *testing.T) {
	for _, name := range []string{"payload.php.png", "payload.PS1.pdf", "document.docm.docx", "image.png.exe", "payload.js.jpg"} {
		_, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: name, ContentType: "image/png", Data: ticketTestImage(t, "png")}}, defaultTicketConfig())
		require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err), name)
	}
}

func TestTicketAttachmentImageOutputAndConcurrencyBounds(t *testing.T) {
	_, _, err := normalizeTicketImage(ticketTestImage(t, "png"), ".png", 10)
	require.Error(t, err)
	for i := 0; i < cap(ticketImageSlots); i++ {
		ticketImageSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(ticketImageSlots); i++ {
			<-ticketImageSlots
		}
	})
	_, _, err = normalizeTicketImage(ticketTestImage(t, "png"), ".png", 1<<20)
	require.ErrorIs(t, err, ErrTicketAttachmentBusy)
	_, err = ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "image.png", Data: ticketTestImage(t, "png")}}, defaultTicketConfig())
	require.Equal(t, "TICKET_ATTACHMENT_BUSY", infraerrors.Reason(err))
	require.Equal(t, 503, infraerrors.Code(err))
}

func TestTicketAttachmentDocxRejectsActivePartsAndXML(t *testing.T) {
	relationship := func(kind, mode, target string) []byte {
		return []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/` + kind + `" TargetMode="` + mode + `" Target="` + target + `"/></Relationships>`)
	}
	tests := []struct {
		name, entry string
		data        []byte
	}{
		{"DTD不定义实体也拒绝", "word/settings.xml", []byte(`<!DOCTYPE settings><settings/>`)},
		{"外部实体", "word/settings.xml", []byte(`<!DOCTYPE settings [<!ENTITY test SYSTEM "file:///fixture-never-read">]><settings>&test;</settings>`)},
		{"处理指令", "word/settings.xml", []byte(`<?xml-stylesheet href="https://example.invalid/payload"?><settings/>`)},
		{"OLE对象", "word/document.xml", []byte(strings.Replace(ticketTestDocument, "<w:body>", "<w:body><w:object/>", 1))},
		{"嵌入压缩包", "word/embeddings/oleObject1.bin", []byte("inert")},
		{"ActiveX", "word/activeX/activeX1.xml", []byte("<control/>")},
		{"嵌入脚本", "word/script.js", []byte("inert")},
		{"外部模板", "word/_rels/settings.xml.rels", relationship("attachedTemplate", "External", "https://example.invalid/template")},
		{"外部图片", "word/_rels/document.xml.rels", relationship("image", "External", "https://example.invalid/image.png")},
		{"脚本链接", "word/_rels/document.xml.rels", relationship("hyperlink", "External", "javascript:inert")},
		{"DDE域", "word/document.xml", []byte(strings.Replace(ticketTestDocument, "<w:body>", `<w:body><w:fldSimple w:instr="D&#68;EAUTO fixture"/>`, 1))},
		{"拆分DDE域", "word/document.xml", []byte(strings.Replace(ticketTestDocument, "<w:body>", `<w:body><w:instrText>D</w:instrText><w:instrText>DEAUTO fixture</w:instrText>`, 1))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries := map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument)}
			entries[tt.entry] = tt.data
			_, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "file.docx", Data: ticketTestZIP(t, entries)}}, defaultTicketConfig())
			require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
		})
	}
	// 普通网页超链接无需服务器加载资源，保留用户文档中的正常链接。
	entries := map[string][]byte{"[Content_Types].xml": []byte(ticketTestContentTypes), "word/document.xml": []byte(ticketTestDocument), "word/_rels/document.xml.rels": relationship("hyperlink", "External", "https://example.com/help")}
	require.NoError(t, validateTicketDocx(ticketTestZIP(t, entries)))
	require.False(t, unsafeTicketWordField(`HYPERLINK "https://example.com/added"`), "普通超链接不能被 LINK 或 DDE 子串误伤")
	_, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: "file.docx", Data: append(ticketTestZIP(t, entries), []byte("inert trailing payload")...)}}, defaultTicketConfig())
	require.Error(t, err)
}

func TestTicketAttachmentDOCRejectsMacroStorageAfterWordStream(t *testing.T) {
	for _, name := range []string{"Macros", "ObjectPool", "VBA", "ActiveX"} {
		data := ticketTestDOC()
		// 危险目录在正常 WordDocument 后面，不能因已找到主文档而漏检。
		entry := data[1280:1408]
		entry[66] = 1
		chars := utf16.Encode([]rune(name))
		for i, value := range chars {
			binary.LittleEndian.PutUint16(entry[i*2:i*2+2], value)
		}
		binary.LittleEndian.PutUint16(entry[64:66], uint16(len(chars)*2+2))
		require.Error(t, validateTicketDOC(data), name)
	}
	data := ticketTestDOC()
	binary.LittleEndian.PutUint16(data[1546:1548], 0x0100)
	require.ErrorContains(t, validateTicketDOC(data), "加密")
	data = ticketTestDOC()
	binary.LittleEndian.PutUint32(data[548:552], 2)
	require.Error(t, validateTicketDOC(data), "拒绝 WordDocument 流链循环")
}
