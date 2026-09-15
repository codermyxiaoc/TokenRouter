package handler

import (
	"archive/zip"
	"bytes"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 用户与管理员预览复用原件访问边界，拒绝请求不得返回附件内容。
func TestTicketPreviewAuthenticationAndHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, role            string
		auth, admin, disabled bool
		err                   error
		status                int
	}{
		{"匿名", "", false, false, false, nil, 401},
		{"本人", "user", true, false, false, nil, 200},
		{"跨用户", "user", true, false, false, service.ErrTicketNotFound, 404},
		{"普通用户后台", "user", true, true, false, nil, 403},
		{"客服", "admin", true, true, false, nil, 200},
		{"管理员个人入口", "admin", true, false, false, nil, 200},
		{"功能关闭", "user", true, false, true, nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := ticketHandlerPDF(0)
			repo := &ticketHandlerRepo{err: tc.err, attachment: &service.TicketAttachment{Filename: "账单.pdf", ContentType: "application/pdf", Data: data}}
			settings := map[string]string{}
			if tc.disabled {
				settings[service.SettingKeyTicketEnabled] = "false"
			}
			r := ticketHandlerTestRouter(repo, settings, tc.role, tc.auth, "GET", "/tickets/:id/attachments/:attachmentId/preview", func(h *TicketHandler) gin.HandlerFunc {
				if tc.admin {
					return h.AdminPreview
				}
				return h.Preview
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/tickets/12/attachments/9/preview", nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
			if tc.status != http.StatusOK {
				require.NotContains(t, w.Body.String(), "%PDF-")
				return
			}
			require.Equal(t, data, w.Body.Bytes())
			require.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
			disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
			require.NoError(t, err)
			require.Equal(t, "inline", disposition)
			require.Equal(t, "账单.pdf", params["filename"])
			require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
			require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
			require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
			require.Contains(t, w.Header().Get("Content-Security-Policy"), "sandbox")
			require.Equal(t, service.TicketActor{UserID: 7, IsAdmin: tc.admin}, repo.actor)
		})
	}
}

// Word 文档返回纯文本而不是 HTML 或原始 Office 二进制，复杂内容不能在站点上下文执行。
func TestTicketPreviewWordIsPlainTextAndOriginalStillDownloads(t *testing.T) {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"word/document.xml":   `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>中文 &lt;script&gt;alert(1)&lt;/script&gt;</w:t></w:r></w:p></w:body></w:document>`,
	} {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(body))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	repo := &ticketHandlerRepo{attachment: &service.TicketAttachment{Filename: "说明.docx", ContentType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Data: buf.Bytes()}}
	for _, preview := range []bool{true, false} {
		r := ticketHandlerTestRouter(repo, nil, "user", true, "GET", "/tickets/:id/attachments/:attachmentId", func(h *TicketHandler) gin.HandlerFunc {
			if preview {
				return h.Preview
			}
			return h.Download
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/tickets/12/attachments/9", nil))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		if preview {
			require.Equal(t, "text/plain; charset=utf-8", w.Header().Get("Content-Type"))
			require.Contains(t, w.Body.String(), "中文 <script>alert(1)</script>")
			require.NotContains(t, w.Body.String(), "w:document")
		} else {
			require.Contains(t, w.Header().Get("Content-Disposition"), "attachment;")
			require.Equal(t, buf.Bytes(), w.Body.Bytes())
		}
	}
}

// 历史数据和客户端提供的 MIME 不能绕过预览安全校验。
func TestTicketPreviewRejectsUnsafeHistoricalContent(t *testing.T) {
	repo := &ticketHandlerRepo{attachment: &service.TicketAttachment{Filename: "receipt.pdf", ContentType: "application/pdf", Data: []byte("<script>inert-preview-test</script>")}}
	r := ticketHandlerTestRouter(repo, nil, "user", true, "GET", "/tickets/:id/attachments/:attachmentId/preview", func(h *TicketHandler) gin.HandlerFunc { return h.Preview })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/tickets/12/attachments/9/preview", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "TICKET_ATTACHMENT_INVALID")
	require.NotContains(t, w.Body.String(), "inert-preview-test")
}
