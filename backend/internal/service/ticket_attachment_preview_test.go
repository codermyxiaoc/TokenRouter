package service

import (
	"bytes"
	"image"
	"testing"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 预览再次清除历史图片尾随内容，返回可靠的图片格式。
func TestPreviewTicketAttachmentRevalidatesImagesAndPDF(t *testing.T) {
	for _, format := range []string{"png", "jpeg", "gif"} {
		data := append(ticketTestImage(t, format), []byte("inert-old-payload")...)
		preview, err := PreviewTicketAttachment(&TicketAttachment{Filename: "image." + format, ContentType: "image/" + format, Data: data})
		require.NoError(t, err)
		require.Equal(t, "image/"+format, preview.ContentType)
		require.NotContains(t, string(preview.Data), "inert-old-payload")
		_, _, err = image.Decode(bytes.NewReader(preview.Data))
		require.NoError(t, err)
	}
	data := ticketTestPDF()
	preview, err := PreviewTicketAttachment(&TicketAttachment{Filename: "../../账单.pdf", ContentType: "application/pdf", Data: data})
	require.NoError(t, err)
	require.Equal(t, "账单.pdf", preview.Name)
	require.Equal(t, data, preview.Data)
}

func TestPreviewTicketAttachmentRejectsUnexpectedTypesAndSize(t *testing.T) {
	for _, file := range []*TicketAttachment{
		nil,
		{Filename: "empty.pdf", ContentType: "application/pdf"},
		{Filename: "large.pdf", ContentType: "application/pdf", Data: make([]byte, (20<<20)+1)},
		{Filename: "image.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")},
		{Filename: "image.html.png", ContentType: "image/png", Data: ticketTestImage(t, "png")},
		{Filename: "image.png", ContentType: "text/html", Data: ticketTestImage(t, "png")},
		{Filename: "word.docx", ContentType: "application/pdf", Data: ticketTestPDF()},
	} {
		preview, err := PreviewTicketAttachment(file)
		require.Equal(t, "TICKET_ATTACHMENT_INVALID", infraerrors.Reason(err))
		require.Nil(t, preview)
	}
}
