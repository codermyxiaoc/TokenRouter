package handler

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// TicketHandler 只接受认证上下文中的身份，附件与消息共同提交。
// @project-doc docs/domains/support_tickets.md#ticket_lifecycle
type TicketHandler struct {
	service *service.TicketService
	config  *service.TicketConfigService
	runtime ticketNotificationSender
}

// 通知端口只接收已提交的事件，便于独立验证权限和幂等触发边界。
type ticketNotificationSender interface {
	NotifyReply(*service.Ticket)
	NotifyClosure(*service.Ticket)
}

func NewTicketHandler(s *service.TicketService, cfg *service.TicketConfigService, runtime *service.TicketRuntime) *TicketHandler {
	h := &TicketHandler{service: s, config: cfg}
	if runtime != nil {
		h.runtime = runtime
	}
	return h
}

// ticketActor 即使管理员从用户入口访问，也仅使用个人权限。
func ticketActor(c *gin.Context, admin bool) (service.TicketActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return service.TicketActor{}, false
	}
	if admin {
		role, _ := middleware.GetUserRoleFromContext(c)
		if role != "admin" {
			response.Forbidden(c, "Administrator access required")
			return service.TicketActor{}, false
		}
	}
	return service.TicketActor{UserID: subject.UserID, IsAdmin: admin}, true
}

func ticketID(c *gin.Context, key string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(key), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid ticket identifier")
		return 0, false
	}
	return id, true
}

func (h *TicketHandler) Config(c *gin.Context)      { h.getConfig(c, false) }
func (h *TicketHandler) AdminConfig(c *gin.Context) { h.getConfig(c, true) }
func (h *TicketHandler) getConfig(c *gin.Context, admin bool) {
	if _, ok := ticketActor(c, admin); !ok {
		return
	}
	cfg, err := h.config.Get(c.Request.Context())
	if !response.ErrorFrom(c, err) {
		response.Success(c, cfg)
	}
}
func (h *TicketHandler) UpdateConfig(c *gin.Context) {
	if _, ok := ticketActor(c, true); !ok {
		return
	}
	var input service.UpdateTicketConfigInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid ticket settings")
		return
	}
	cfg, err := h.config.Update(c.Request.Context(), &input)
	if !response.ErrorFrom(c, err) {
		response.Success(c, cfg)
	}
}
func (h *TicketHandler) List(c *gin.Context)      { h.list(c, false) }
func (h *TicketHandler) AdminList(c *gin.Context) { h.list(c, true) }
func (h *TicketHandler) list(c *gin.Context, admin bool) {
	actor, ok := ticketActor(c, admin)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	result, err := h.service.List(c.Request.Context(), actor, service.TicketListFilter{Page: page, PageSize: size, Type: c.Query("type"), Status: c.Query("status"), Priority: c.Query("priority"), Search: c.Query("q")})
	if !response.ErrorFrom(c, err) {
		response.Success(c, result)
	}
}
func (h *TicketHandler) Get(c *gin.Context)      { h.get(c, false) }
func (h *TicketHandler) AdminGet(c *gin.Context) { h.get(c, true) }
func (h *TicketHandler) get(c *gin.Context, admin bool) {
	actor, ok := ticketActor(c, admin)
	if !ok {
		return
	}
	id, ok := ticketID(c, "id")
	if !ok {
		return
	}
	ticket, err := h.service.Get(c.Request.Context(), actor, id)
	if !response.ErrorFrom(c, err) {
		response.Success(c, ticket)
	}
}
func (h *TicketHandler) Create(c *gin.Context) {
	actor, ok := ticketActor(c, false)
	if !ok {
		return
	}
	var input service.CreateTicketInput
	files, ok := h.readInput(c, &input)
	if !ok {
		return
	}
	input.Attachments = files
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	ticket, err := h.service.Create(c.Request.Context(), actor, &input)
	if !response.ErrorFrom(c, err) {
		response.Created(c, ticket)
	}
}
func (h *TicketHandler) Reply(c *gin.Context)      { h.reply(c, false) }
func (h *TicketHandler) AdminReply(c *gin.Context) { h.reply(c, true) }
func (h *TicketHandler) reply(c *gin.Context, admin bool) {
	actor, ok := ticketActor(c, admin)
	if !ok {
		return
	}
	id, ok := ticketID(c, "id")
	if !ok {
		return
	}
	var input service.ReplyTicketInput
	files, ok := h.readInput(c, &input)
	if !ok {
		return
	}
	input.Attachments = files
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	ticket, err := h.service.Reply(c.Request.Context(), actor, id, &input)
	if response.ErrorFrom(c, err) {
		return
	}
	if admin && ticket.ReplyCreated && h.runtime != nil {
		h.runtime.NotifyReply(ticket)
	}
	response.Success(c, ticket)
}
func (h *TicketHandler) Cancel(c *gin.Context) { h.close(c, false, service.TicketStatusCancelled) }

// AdminCancel 沿用后台认证与终态规则，不能把已完成工单改为已撤销。
func (h *TicketHandler) AdminCancel(c *gin.Context) { h.close(c, true, service.TicketStatusCancelled) }
func (h *TicketHandler) Complete(c *gin.Context)    { h.close(c, false, service.TicketStatusCompleted) }
func (h *TicketHandler) AdminComplete(c *gin.Context) {
	h.close(c, true, service.TicketStatusCompleted)
}
func (h *TicketHandler) close(c *gin.Context, admin bool, status string) {
	actor, ok := ticketActor(c, admin)
	if !ok {
		return
	}
	id, ok := ticketID(c, "id")
	if !ok {
		return
	}
	ticket, err := h.service.Close(c.Request.Context(), actor, id, status)
	if response.ErrorFrom(c, err) {
		return
	}
	// 仅管理员首次完成或撤销成功后通知用户；个人结单与网络重试不发邮件。
	if admin && ticket.ClosureCreated && h.runtime != nil {
		h.runtime.NotifyClosure(ticket)
	}
	response.Success(c, ticket)
}
func (h *TicketHandler) Update(c *gin.Context) {
	actor, ok := ticketActor(c, true)
	if !ok {
		return
	}
	id, ok := ticketID(c, "id")
	if !ok {
		return
	}
	var input service.UpdateTicketInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid ticket update")
		return
	}
	ticket, err := h.service.Update(c.Request.Context(), actor, id, &input)
	if !response.ErrorFrom(c, err) {
		response.Success(c, ticket)
	}
}
func (h *TicketHandler) Download(c *gin.Context)      { h.download(c, false) }
func (h *TicketHandler) AdminDownload(c *gin.Context) { h.download(c, true) }
func (h *TicketHandler) Preview(c *gin.Context)       { h.serveAttachment(c, false, true) }
func (h *TicketHandler) AdminPreview(c *gin.Context)  { h.serveAttachment(c, true, true) }
func (h *TicketHandler) download(c *gin.Context, admin bool) {
	h.serveAttachment(c, admin, false)
}

// 预览和原件下载共用归属、角色与功能开关校验，不生成公开地址或带令牌的链接。
// @project-doc docs/domains/support_tickets.md#ticket_attachments
func (h *TicketHandler) serveAttachment(c *gin.Context, admin, preview bool) {
	c.Header("Cache-Control", "private, no-store")
	actor, ok := ticketActor(c, admin)
	if !ok {
		return
	}
	id, ok := ticketID(c, "id")
	if !ok {
		return
	}
	attachmentID, ok := ticketID(c, "attachmentId")
	if !ok {
		return
	}
	file, err := h.service.Attachment(c.Request.Context(), actor, id, attachmentID)
	if response.ErrorFrom(c, err) {
		return
	}
	name, contentType, data := file.Filename, file.ContentType, file.Data
	disposition := "attachment"
	if preview {
		result, err := service.PreviewTicketAttachment(file)
		if response.ErrorFrom(c, err) {
			return
		}
		name, contentType, data = result.Name, result.ContentType, result.Data
		disposition = "inline"
	}
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	// 即使浏览器忽略下载处置，也不能在站点身份下执行附件主动内容。
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Referrer-Policy", "no-referrer")
	c.Data(http.StatusOK, contentType, data)
}

// readInput 在解析 multipart 前限制整个请求，临时文件在返回前清理。
// @project-doc docs/domains/support_tickets.md#ticket_attachments
func (h *TicketHandler) readInput(c *gin.Context, dst any) ([]service.TicketAttachmentUpload, bool) {
	cfg, err := h.config.Get(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return nil, false
	}
	// 关闭时在读取上传正文之前拒绝，避免无效请求占用附件解析资源。
	if !cfg.Enabled {
		response.ErrorFrom(c, service.ErrTicketDisabled)
		return nil, false
	}
	mediaType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if mediaType == "application/json" {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		decoder := json.NewDecoder(c.Request.Body)
		if err := decoder.Decode(dst); err != nil {
			response.BadRequest(c, "Invalid ticket payload")
			return nil, false
		}
		// 必须读到真正的请求结束，不能忽略第二个 JSON 或被截断的超大尾随正文。
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			response.BadRequest(c, "Invalid ticket payload")
			return nil, false
		}
		return nil, true
	}
	if mediaType != "multipart/form-data" {
		response.BadRequest(c, "Expected multipart/form-data")
		return nil, false
	}
	// 附件数量和单文件限制也收紧解析前的请求上限，禁用附件时不接收大文件后再拒绝。
	maxFiles := int64(cfg.MaxAttachments) * int64(cfg.MaxAttachmentSizeMB) << 20
	if maxFiles > service.TicketMaxTotalAttachmentBytes {
		maxFiles = service.TicketMaxTotalAttachmentBytes
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxFiles+(1<<20))
	err = c.Request.ParseMultipartForm(8 << 20)
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			response.ErrorWithDetails(c, 413, "工单上传内容超过大小限制", "TICKET_ATTACHMENT_TOO_LARGE", nil)
		} else {
			response.BadRequest(c, "Invalid multipart upload")
		}
		return nil, false
	}
	form := c.Request.MultipartForm
	payloads := form.Value["payload"]
	if len(payloads) != 1 || len(payloads[0]) > 1<<20 || json.Unmarshal([]byte(payloads[0]), dst) != nil {
		response.BadRequest(c, "Invalid ticket payload")
		return nil, false
	}
	for name := range form.File {
		if name != "files" {
			response.BadRequest(c, "Unexpected attachment field")
			return nil, false
		}
	}
	headers := form.File["files"]
	if len(headers) > cfg.MaxAttachments {
		response.ErrorWithDetails(c, 400, "附件数量超过限制", "TICKET_ATTACHMENT_LIMIT", nil)
		return nil, false
	}
	files := make([]service.TicketAttachmentUpload, 0, len(headers))
	var total int64
	limit := int64(cfg.MaxAttachmentSizeMB) << 20
	for _, header := range headers {
		if header.Size > limit {
			response.ErrorWithDetails(c, 400, "附件大小超过限制", "TICKET_ATTACHMENT_TOO_LARGE", nil)
			return nil, false
		}
		file, err := header.Open()
		if err != nil {
			response.BadRequest(c, "Cannot read attachment")
			return nil, false
		}
		data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
		_ = file.Close()
		total += int64(len(data))
		if readErr != nil || int64(len(data)) > limit || total > service.TicketMaxTotalAttachmentBytes {
			response.ErrorWithDetails(c, 400, "附件大小超过限制", "TICKET_ATTACHMENT_TOO_LARGE", nil)
			return nil, false
		}
		files = append(files, service.TicketAttachmentUpload{Name: strings.TrimSpace(header.Filename), ContentType: header.Header.Get("Content-Type"), Data: data})
	}
	return files, true
}
