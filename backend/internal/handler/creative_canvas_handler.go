package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/response"
	"github.com/TokenFlux/TokenRouter/internal/server/middleware"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
)

// 云画布接口仅使用用户 JWT；任何用户 ID 和素材归属均由服务端重新验证。
type CreativeCanvasHandler struct {
	service *service.CreativeCanvasService
}

func NewCreativeCanvasHandler(s *service.CreativeCanvasService) *CreativeCanvasHandler {
	return &CreativeCanvasHandler{service: s}
}

// ConfigureTextGateway 在网关路由完成装配后绑定进程内文本桥，避免 service 反向依赖 handler。
func (h *CreativeCanvasHandler) ConfigureTextGateway(gateway service.CreativeTextGateway) {
	if h != nil && h.service != nil {
		h.service.SetTextGateway(gateway)
	}
}

// ConfigureGrokVideoGateway 只绑定原 Grok 媒体链路，不改变现有 Video 平台执行器。
func (h *CreativeCanvasHandler) ConfigureGrokVideoGateway(gateway service.CreativeGrokVideoGateway) {
	if h != nil && h.service != nil {
		h.service.SetGrokVideoGateway(gateway)
	}
}
func canvasUser(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "User not authenticated")
		return 0, false
	}
	return subject.UserID, true
}
func canvasDecode(c *gin.Context, value any, limit int64) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		response.ErrorFrom(c, service.ErrCanvasInvalid)
		return false
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		response.ErrorFrom(c, service.ErrCanvasInvalid)
		return false
	}
	return true
}
func canvasPagination(c *gin.Context) (int, int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size, (page - 1) * size
}
func canvasList(c *gin.Context, items any, total int64, page, size int) {
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

func (h *CreativeCanvasHandler) Create(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	var input struct {
		Title    string          `json:"title"`
		Document json.RawMessage `json:"document"`
	}
	if !canvasDecode(c, &input, service.CreativeCanvasDocumentLimit+4096) {
		return
	}
	v, err := h.service.Create(c.Request.Context(), u, input.Title, input.Document)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Created(c, v)
}
func (h *CreativeCanvasHandler) List(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	page, size, offset := canvasPagination(c)
	items, total, err := h.service.List(c.Request.Context(), u, size, offset)
	if response.ErrorFrom(c, err) {
		return
	}
	canvasList(c, items, total, page, size)
}
func (h *CreativeCanvasHandler) Get(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	v, err := h.service.Get(c.Request.Context(), u, c.Param("id"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, v)
}
func (h *CreativeCanvasHandler) Update(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	var input struct {
		Title    string          `json:"title"`
		Document json.RawMessage `json:"document"`
		Revision int64           `json:"revision"`
	}
	if !canvasDecode(c, &input, service.CreativeCanvasDocumentLimit+4096) {
		return
	}
	if len(input.Document) == 0 {
		response.ErrorFrom(c, service.ErrCanvasInvalid)
		return
	}
	v, err := h.service.Update(c.Request.Context(), u, c.Param("id"), input.Title, input.Document, input.Revision)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, v)
}
func (h *CreativeCanvasHandler) Delete(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(c.Query("revision"), 10, 64)
	if err != nil {
		response.ErrorFrom(c, service.ErrCanvasInvalid)
		return
	}
	if response.ErrorFrom(c, h.service.Delete(c.Request.Context(), u, c.Param("id"), revision)) {
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

func (h *CreativeCanvasHandler) UploadAsset(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.CreativeCanvasUploadLimit+(1<<20))
	if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
		if c.Request.MultipartForm != nil {
			_ = c.Request.MultipartForm.RemoveAll()
		}
		response.ErrorFrom(c, service.ErrCanvasQuota)
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	form := c.Request.MultipartForm
	if len(form.File) != 1 || len(form.File["file"]) != 1 || len(form.Value) != 1 || len(form.Value["canvas_id"]) != 1 {
		response.ErrorFrom(c, service.ErrCanvasInvalid)
		return
	}
	fileHeader := form.File["file"][0]
	if fileHeader.Size <= 0 || fileHeader.Size > service.CreativeCanvasUploadLimit {
		response.ErrorFrom(c, service.ErrCanvasQuota)
		return
	}
	reader, err := fileHeader.Open()
	if response.ErrorFrom(c, err) {
		return
	}
	defer reader.Close()
	a, err := h.service.UploadAsset(c.Request.Context(), u, form.Value["canvas_id"][0], reader, fileHeader.Size)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Created(c, a)
}
func (h *CreativeCanvasHandler) ListAssets(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	page, size, offset := canvasPagination(c)
	items, total, err := h.service.ListAssets(c.Request.Context(), u, c.Query("canvas_id"), size, offset)
	if response.ErrorFrom(c, err) {
		return
	}
	canvasList(c, items, total, page, size)
}
func (h *CreativeCanvasHandler) GetAsset(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	a, err := h.service.GetAsset(c.Request.Context(), u, c.Param("id"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, a)
}
func (h *CreativeCanvasHandler) DeleteAsset(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	if response.ErrorFrom(c, h.service.DeleteAsset(c.Request.Context(), u, c.Param("id"))) {
		return
	}
	response.Success(c, gin.H{"deleted": true})
}
func (h *CreativeCanvasHandler) AssetContent(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	byteRange := c.GetHeader("Range")
	if len(byteRange) > 100 || strings.Contains(byteRange, ",") {
		c.Status(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	a, reader, err := h.service.OpenAsset(c.Request.Context(), u, c.Param("id"))
	if response.ErrorFrom(c, err) {
		return
	}
	defer reader.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "default-src 'none'; sandbox")
	c.Header("Content-Type", a.MIMEType)
	c.Header("Content-Disposition", `inline; filename="asset"`)
	controller := http.NewResponseController(c.Writer)
	if controller.SetWriteDeadline(time.Now().Add(2*time.Minute)) == nil {
		defer controller.SetWriteDeadline(time.Time{})
	}
	http.ServeContent(c.Writer, c.Request, "asset", a.CreatedAt, reader)
}

func (h *CreativeCanvasHandler) CreateExecution(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	var input struct {
		CanvasID string                                `json:"canvas_id"`
		NodeID   string                                `json:"node_id"`
		Kind     string                                `json:"kind"`
		Params   service.CreativeCanvasExecutionParams `json:"params"`
	}
	if !canvasDecode(c, &input, 128<<10) {
		return
	}
	e, err := h.service.CreateExecution(c.Request.Context(), u, input.CanvasID, input.NodeID, input.Kind, input.Params, c.GetHeader("Idempotency-Key"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Accepted(c, e)
}

// CreateExecutionBatch 一次受理整批图片，数量由服务端模型合同校验，不能由客户端放大单次预扣。
func (h *CreativeCanvasHandler) CreateExecutionBatch(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	var input struct {
		CanvasID string                                `json:"canvas_id"`
		NodeID   string                                `json:"node_id"`
		Kind     string                                `json:"kind"`
		Count    int                                   `json:"count"`
		Params   service.CreativeCanvasExecutionParams `json:"params"`
	}
	if !canvasDecode(c, &input, 128<<10) {
		return
	}
	batch, err := h.service.CreateExecutionBatch(c.Request.Context(), u, input.CanvasID, input.NodeID, input.Kind, input.Params, input.Count, c.GetHeader("Idempotency-Key"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Accepted(c, batch)
}

func (h *CreativeCanvasHandler) ListExecutions(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	page, size, offset := canvasPagination(c)
	items, total, err := h.service.ListExecutions(c.Request.Context(), u, c.Query("canvas_id"), size, offset)
	if response.ErrorFrom(c, err) {
		return
	}
	canvasList(c, items, total, page, size)
}
func (h *CreativeCanvasHandler) GetExecution(c *gin.Context) {
	u, ok := canvasUser(c)
	if !ok {
		return
	}
	e, err := h.service.GetExecution(c.Request.Context(), u, c.Param("id"))
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, e)
}
