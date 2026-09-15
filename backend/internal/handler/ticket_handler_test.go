package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 窄桩只实现当前测试涉及的端口，误访问其他依赖会直接暴露。
type ticketHandlerSettings struct {
	service.SettingRepository
	values map[string]string
}

func (s *ticketHandlerSettings) GetMultiple(context.Context, []string) (map[string]string, error) {
	return s.values, nil
}

type ticketHandlerRepo struct {
	service.TicketRepository
	actor          service.TicketActor
	input          *service.CreateTicketInput
	err            error
	closureCreated bool
	attachment     *service.TicketAttachment
}

func (r *ticketHandlerRepo) Close(_ context.Context, actor service.TicketActor, id int64, status string) (*service.Ticket, error) {
	r.actor = actor
	return &service.Ticket{ID: id, Status: status, ClosureCreated: r.closureCreated}, r.err
}

type ticketHandlerNotifier struct {
	closures []*service.Ticket
}

func (n *ticketHandlerNotifier) NotifyReply(*service.Ticket) {}
func (n *ticketHandlerNotifier) NotifyClosure(ticket *service.Ticket) {
	n.closures = append(n.closures, ticket)
}

func (r *ticketHandlerRepo) Create(_ context.Context, actor service.TicketActor, in *service.CreateTicketInput, _ int, _ string) (*service.Ticket, error) {
	r.actor = actor
	r.input = in
	return &service.Ticket{ID: 12, UserID: actor.UserID}, r.err
}
func (r *ticketHandlerRepo) Attachment(_ context.Context, actor service.TicketActor, _, _ int64) (*service.TicketAttachment, error) {
	r.actor = actor
	if r.attachment != nil {
		return r.attachment, r.err
	}
	return &service.TicketAttachment{Filename: "账单.pdf", ContentType: "application/pdf", Data: []byte("document")}, r.err
}

func ticketHandlerTestRouter(repo *ticketHandlerRepo, values map[string]string, role string, authenticated bool, method, path string, action func(*TicketHandler) gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	cfg := service.NewTicketConfigService(&ticketHandlerSettings{values: values})
	h := NewTicketHandler(service.NewTicketService(repo, cfg), cfg, nil)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if authenticated {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), role)
		}
	})
	r.Handle(method, path, action(h))
	return r
}

func ticketMultipart(t *testing.T, payload string, files int) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("payload", payload))
	for i := 0; i < files; i++ {
		part, err := writer.CreateFormFile("files", "receipt.pdf")
		require.NoError(t, err)
		_, err = part.Write(ticketHandlerPDF(0))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

// 测试使用包含真实交叉引用表的静态 PDF，大流用于验证临时文件清理。
func ticketHandlerPDF(padding int) []byte {
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", padding, strings.Repeat(" ", padding)),
	}
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprint(&out, "xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return out.Bytes()
}

func TestTicketHandlerCreateMultipartUsesAuthenticatedOwner(t *testing.T) {
	repo := &ticketHandlerRepo{}
	r := ticketHandlerTestRouter(repo, nil, "admin", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
	body, contentType := ticketMultipart(t, `{"type":"technical","title":"调用异常","content":"详细问题","priority":"high","user_id":999,"is_admin":true}`, 1)
	req := httptest.NewRequest("POST", "/tickets", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Idempotency-Key", "create-test-1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 201, w.Code, w.Body.String())
	require.Equal(t, service.TicketActor{UserID: 7}, repo.actor)
	require.Equal(t, "create-test-1", repo.input.IdempotencyKey)
	require.Len(t, repo.input.Attachments, 1)
	require.Equal(t, "application/pdf", repo.input.Attachments[0].ContentType)
}

func TestTicketHandlerUploadLimitsAndMalformedPayload(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		files         int
		settings      map[string]string
	}{
		{"禁用附件", `{"type":"technical","title":"a","content":"b","priority":"normal"}`, 1, map[string]string{service.SettingKeyTicketMaxAttachments: "0"}},
		{"超过数量", `{}`, 2, map[string]string{service.SettingKeyTicketMaxAttachments: "1"}},
		{"错误正文", `invalid-json`, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &ticketHandlerRepo{}
			r := ticketHandlerTestRouter(repo, tc.settings, "user", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
			body, contentType := ticketMultipart(t, tc.payload, tc.files)
			req := httptest.NewRequest("POST", "/tickets", body)
			req.Header.Set("Content-Type", contentType)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, 400, w.Code)
			require.Nil(t, repo.input)
		})
	}
}

// JSON与multipart直连同样受服务端标题限制，返回明确错误码且不写入仓储。
func TestTicketHandlerRejectsLongTitle(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"type": "technical", "title": strings.Repeat("字", 51), "content": "问题说明"})
	require.NoError(t, err)
	for _, multipartBody := range []bool{false, true} {
		repo := &ticketHandlerRepo{}
		r := ticketHandlerTestRouter(repo, nil, "user", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
		body, contentType := bytes.NewBuffer(payload), "application/json"
		if multipartBody {
			body, contentType = ticketMultipart(t, string(payload), 0)
		}
		req := httptest.NewRequest("POST", "/tickets", body)
		req.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, 400, w.Code, w.Body.String())
		require.Contains(t, w.Body.String(), "TICKET_TITLE_TOO_LONG")
		require.Nil(t, repo.input)
	}
}

func TestTicketHandlerAttachmentAuthAndPrivateHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, role  string
		auth, admin bool
		err         error
		status      int
	}{
		{"未登录", "", false, false, nil, 401},
		{"个人下载", "user", true, false, nil, 200},
		{"越权工单", "user", true, false, service.ErrTicketNotFound, 404},
		{"普通用户访问后台", "user", true, true, nil, 403},
		{"管理员下载", "admin", true, true, nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &ticketHandlerRepo{err: tc.err}
			r := ticketHandlerTestRouter(repo, nil, tc.role, tc.auth, "GET", "/tickets/:id/attachments/:attachmentId", func(h *TicketHandler) gin.HandlerFunc {
				if tc.admin {
					return h.AdminDownload
				}
				return h.Download
			})
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest("GET", "/tickets/12/attachments/9", nil))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status == 200 {
				require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
				require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
				require.Contains(t, w.Header().Get("Content-Security-Policy"), "sandbox")
				require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
				require.Contains(t, w.Header().Get("Content-Disposition"), "attachment;")
				require.Equal(t, "document", w.Body.String())
				require.Equal(t, tc.admin, repo.actor.IsAdmin)
			}
		})
	}
}

func TestTicketHandlerOpenLimitPreservesReason(t *testing.T) {
	repo := &ticketHandlerRepo{err: service.ErrTicketOpenLimit}
	r := ticketHandlerTestRouter(repo, nil, "user", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
	req := httptest.NewRequest("POST", "/tickets", bytes.NewBufferString(`{"type":"technical","title":"a","content":"b","priority":"normal"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	var data map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &data))
	require.Equal(t, "TICKET_OPEN_LIMIT", data["reason"])
}

// 结单身份必须来自认证上下文和入口权限，不能由请求正文伪造。
func TestTicketHandlerCloseUsesAuthenticatedActor(t *testing.T) {
	for _, target := range []string{service.TicketStatusCompleted, service.TicketStatusCancelled} {
		for _, tc := range []struct {
			name, role  string
			auth, admin bool
			err         error
			status      int
		}{
			{"未登录", "", false, false, nil, 401},
			{"用户结单", "user", true, false, nil, 200},
			{"管理员个人入口仍是用户", "admin", true, false, nil, 200},
			{"普通用户不能在后台结单", "user", true, true, nil, 403},
			{"客服结单", "admin", true, true, nil, 200},
			{"越权工单", "user", true, false, service.ErrTicketNotFound, 404},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				repo := &ticketHandlerRepo{err: tc.err}
				r := ticketHandlerTestRouter(repo, nil, tc.role, tc.auth, "POST", "/tickets/:id/close", func(h *TicketHandler) gin.HandlerFunc {
					if target == service.TicketStatusCompleted {
						if tc.admin {
							return h.AdminComplete
						}
						return h.Complete
					}
					if tc.admin {
						return h.AdminCancel
					}
					return h.Cancel
				})
				req := httptest.NewRequest("POST", "/tickets/12/close", strings.NewReader(`{"user_id":999,"is_admin":true,"closed_by":999,"closed_by_role":"system"}`))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, tc.status, w.Code, w.Body.String())
				if tc.status == http.StatusUnauthorized || tc.status == http.StatusForbidden {
					require.Zero(t, repo.actor.UserID)
					return
				}
				require.Equal(t, service.TicketActor{UserID: 7, IsAdmin: tc.admin}, repo.actor)
				if tc.status == http.StatusOK {
					var result struct {
						Data service.Ticket `json:"data"`
					}
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
					require.Equal(t, target, result.Data.Status)
					require.EqualValues(t, 12, result.Data.ID)
				}
			})
		}
	}
}

// 后台首次关单成功才发送通知；普通入口、事务错误和终态重试都不能触发。
func TestTicketHandlerClosureNotificationBoundary(t *testing.T) {
	for _, target := range []string{service.TicketStatusCompleted, service.TicketStatusCancelled} {
		for _, tc := range []struct {
			name           string
			admin, created bool
			err            error
			want           int
		}{
			{"客服新关单", true, true, nil, 1},
			{"重复关单", true, false, nil, 0},
			{"个人入口", false, true, nil, 0},
			{"事务失败", true, true, service.ErrTicketClosed, 0},
		} {
			t.Run(target+"/"+tc.name, func(t *testing.T) {
				repo := &ticketHandlerRepo{closureCreated: tc.created, err: tc.err}
				notifier := &ticketHandlerNotifier{}
				r := ticketHandlerTestRouter(repo, nil, "admin", true, "POST", "/tickets/:id/close", func(h *TicketHandler) gin.HandlerFunc {
					h.runtime = notifier
					if target == service.TicketStatusCompleted {
						if tc.admin {
							return h.AdminComplete
						}
						return h.Complete
					}
					if tc.admin {
						return h.AdminCancel
					}
					return h.Cancel
				})
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest("POST", "/tickets/12/close", nil))
				if tc.err != nil {
					require.Equal(t, http.StatusConflict, w.Code)
				} else {
					require.Equal(t, http.StatusOK, w.Code)
				}
				require.Len(t, notifier.closures, tc.want)
				if tc.want == 1 {
					require.Equal(t, target, notifier.closures[0].Status)
				}
				require.NotContains(t, w.Body.String(), "ClosureCreated")
				require.NotContains(t, w.Body.String(), "closure_created")
			})
		}
	}
}

func TestTicketHandlerJSONRejectsTrailingOrOversizedBody(t *testing.T) {
	const payload = `{"type":"technical","title":"调用异常","content":"详细问题","priority":"normal"}`
	for _, suffix := range []string{` {"is_admin":true}`, ` invalid`, strings.Repeat(" ", 1<<20)} {
		repo := &ticketHandlerRepo{}
		r := ticketHandlerTestRouter(repo, nil, "user", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
		req := httptest.NewRequest("POST", "/tickets", strings.NewReader(payload+suffix))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		require.Nil(t, repo.input)
	}
}

func TestTicketHandlerMultipartRemovesSpilledFiles(t *testing.T) {
	// 超过 8 MiB 的单附件会实际进入临时目录，成功及各失败路径都必须清理。
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	for _, scenario := range []string{"成功", "正文损坏", "上传截断"} {
		t.Run(scenario, func(t *testing.T) {
			repo := &ticketHandlerRepo{}
			r := ticketHandlerTestRouter(repo, nil, "user", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			payload := `{"type":"technical","title":"调用异常","content":"详细问题","priority":"normal"}`
			if scenario == "正文损坏" {
				payload = "invalid"
			}
			require.NoError(t, writer.WriteField("payload", payload))
			part, err := writer.CreateFormFile("files", "receipt.pdf")
			require.NoError(t, err)
			_, err = part.Write(ticketHandlerPDF(9 << 20))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			if scenario == "上传截断" {
				closing := bytes.LastIndex(body.Bytes(), []byte("\r\n--"+writer.Boundary()+"--"))
				require.Positive(t, closing)
				body.Truncate(closing)
			}
			req := httptest.NewRequest("POST", "/tickets", &body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if scenario == "成功" {
				require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
			} else {
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			}
			entries, err := os.ReadDir(tmp)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestTicketHandlerRejectsOversizedUploadBeforeConfiguredLimit(t *testing.T) {
	repo := &ticketHandlerRepo{}
	r := ticketHandlerTestRouter(repo, map[string]string{service.SettingKeyTicketMaxAttachments: "0"}, "user", true, "POST", "/tickets", func(h *TicketHandler) gin.HandlerFunc { return h.Create })
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("payload", `{}`))
	part, err := writer.CreateFormFile("files", "large.pdf")
	require.NoError(t, err)
	_, err = part.Write(make([]byte, 2<<20))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest("POST", "/tickets", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code, w.Body.String())
	require.Nil(t, repo.input)
}
