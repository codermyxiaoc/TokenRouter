package httputil

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/pkg/jsonutil"
	"github.com/klauspost/compress/zstd"
)

const (
	requestBodyReadInitCap    = 512
	requestBodyReadMaxInitCap = 1 << 20
	jsonUTF8BOMLen            = 3
	// maxDecompressedBodySize limits the decompressed request body to 64 MB
	// to prevent decompression bomb attacks.
	maxDecompressedBodySize = 64 << 20
)

// prereadBody 保存已解压的正文，供选组、模型映射与业务处理器共享读取。
type prereadBody struct {
	*bytes.Reader
	data []byte
}

func (p *prereadBody) Close() error { return nil }

// PrereadRequestBytes 返回上一步已回填的正文；流式调用方仍可以通过 Body 顺序读取。
func PrereadRequestBytes(req *http.Request) ([]byte, bool) {
	body, ok := req.Body.(*prereadBody)
	if !ok {
		return nil, false
	}
	return body.data, true
}

// ResetRequestBody 用已读取的完整正文重置请求，并同步传输长度。
func ResetRequestBody(req *http.Request, data []byte) {
	req.Body = &prereadBody{Reader: bytes.NewReader(data), data: data}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Length", strconv.Itoa(len(data)))
}

// PrepareGatewayRequestBody 在任何路由改写前按实际协议检查并回填原始正文。
// 无需解析自由文本或二进制，JSON 的 BOM 仅在确定为 JSON 时移除。
func PrepareGatewayRequestBody(req *http.Request, limit int64) error {
	data, err := readRequestBodyWithPrealloc(req, limit)
	if err != nil {
		return err
	}
	if limit > 0 && int64(len(data)) > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	doc := jsonutil.TrimUTF8BOM(data)
	if len(doc) != len(data) && jsonutil.LooksLikeJSON(doc) {
		data = doc
	}
	contentType := req.Header.Get("Content-Type")
	batchMetadata := req.URL != nil && req.URL.Path == "/v1/images/batches"
	if _, conflict := jsonutil.ConflictingField(contentType, data, batchMetadata); conflict {
		return jsonutil.ErrDuplicateField
	}
	// 声明为表单但正文确为 JSON 的客户端也必须先走 JSON 选组和模型映射。
	// 真正的表单含有独立 part；有双重解读的正文已在上面被拒绝。
	if jsonutil.LooksLikeJSON(data) && strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/") {
		if mediaType, _, _ := mime.ParseMediaType(contentType); strings.HasPrefix(mediaType, "multipart/") {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	ResetRequestBody(req, data)
	return nil
}

// BodyTooLargeMessage 让提前准入与业务处理器使用同一上限提示。
func BodyTooLargeMessage(limit int64) string {
	const mb = 1 << 20
	if limit >= mb {
		return fmt.Sprintf("Request body too large, limit is %dMB", limit/mb)
	}
	return fmt.Sprintf("Request body too large, limit is %dB", limit)
}

// ReadRequestBodyWithPrealloc reads request body with preallocated buffer based
// on content length, transparently decoding any Content-Encoding the upstream
// client used to compress the body (zstd, gzip, deflate).
func ReadRequestBodyWithPrealloc(req *http.Request) ([]byte, error) {
	body, err := readRequestBodyWithPrealloc(req, 0)
	if err != nil {
		return nil, err
	}
	return validateRoutingRequestBody(body)
}

// 读取与解压不自行规范化；调用方在最终解析边界校验，避免宽容入口对同一正文重复复制。
func readRequestBodyWithPrealloc(req *http.Request, decodedLimit int64) ([]byte, error) {
	if req == nil || req.Body == nil {
		return nil, nil
	}

	if data, ok := PrereadRequestBytes(req); ok {
		return data, nil
	}

	capHint := requestBodyReadInitCap
	if req.ContentLength > 0 {
		switch {
		case req.ContentLength < int64(requestBodyReadInitCap):
			capHint = requestBodyReadInitCap
		case req.ContentLength > int64(requestBodyReadMaxInitCap):
			capHint = requestBodyReadMaxInitCap
		default:
			capHint = int(req.ContentLength)
		}
	}

	raw, err := readRequestBodyChunks(req.Body, capHint, req.ContentLength)
	if err != nil {
		return nil, err
	}

	enc := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Encoding")))
	if enc == "" || enc == "identity" {
		return raw, nil
	}

	if decodedLimit <= 0 || decodedLimit > maxDecompressedBodySize {
		decodedLimit = maxDecompressedBodySize
	}
	decoded, err := decompressRequestBody(enc, raw, decodedLimit)
	if err != nil {
		return nil, fmt.Errorf("decode Content-Encoding %q: %w", enc, err)
	}

	req.Header.Del("Content-Encoding")
	req.Header.Del("Content-Length")
	req.ContentLength = int64(len(decoded))

	return decoded, nil
}

// 请求读体完成后先拒绝歧义模型，保证模型重定向、调度和供应商转换看到同一字段。
func validateRoutingRequestBody(body []byte) ([]byte, error) {
	if err := jsonutil.ValidateRoutingModel(body); err != nil {
		return nil, err
	}
	return body, nil
}

// readRequestBodyChunks 按有界块接收请求，完成后只组装一次精确大小的结果。
// Content-Length 仅用于容量提示；始终读到 EOF，保留 MaxBytesReader 和传输错误。
func readRequestBodyChunks(reader io.Reader, initialCapacity int, contentLength int64) ([]byte, error) {
	capacity := initialCapacity
	var chunks [][]byte
	total := 0
	for {
		chunkCapacity := capacity
		if remaining := contentLength - int64(total); remaining >= 0 && remaining < int64(chunkCapacity) {
			chunkCapacity = int(remaining) + 1
		}
		chunk := make([]byte, chunkCapacity)
		n := 0
		var err error
		for n < len(chunk) && err == nil {
			var read int
			read, err = reader.Read(chunk[n:])
			n += read
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		if n > 0 {
			chunks = append(chunks, chunk[:n])
			total += n
		}
		if err != nil {
			if len(chunks) == 0 {
				return chunk[:0], nil
			}
			if len(chunks) == 1 {
				return chunks[0], nil
			}
			body := make([]byte, total)
			offset := 0
			for _, part := range chunks {
				offset += copy(body[offset:], part)
			}
			return body, nil
		}
		if capacity < requestBodyReadMaxInitCap {
			capacity *= 2
			if capacity > requestBodyReadMaxInitCap {
				capacity = requestBodyReadMaxInitCap
			}
		}
	}
}

// ReadLenientJSONRequestBodyWithPrealloc 读取请求体，并在严格 JSON
// 校验前转义字符串中的原始控制字节。
func ReadLenientJSONRequestBodyWithPrealloc(req *http.Request, maxNormalizedBytes int64) ([]byte, error) {
	body, err := readRequestBodyWithPrealloc(req, 0)
	if err != nil {
		return nil, err
	}
	return NormalizeLenientJSONRequestBody(body, maxNormalizedBytes)
}

func decompressRequestBody(encoding string, raw []byte, limit int64) ([]byte, error) {
	switch encoding {
	case "zstd":
		dec, err := zstd.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		return readDecodedBody(dec, limit)
	case "gzip", "x-gzip":
		gr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer func() { _ = gr.Close() }()
		return readDecodedBody(gr, limit)
	case "deflate":
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		defer func() { _ = zr.Close() }()
		return readDecodedBody(zr, limit)
	default:
		return nil, errors.New("unsupported Content-Encoding")
	}
}

// readDecodedBody 额外读取一个字节区分恰好到达上限与正文被截断。
func readDecodedBody(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err == nil && int64(len(data)) > limit {
		return nil, &http.MaxBytesError{Limit: limit}
	}
	return data, err
}

// NormalizeLenientJSONRequestBody 转义部分异常 OpenAI 兼容客户端
// 直接写入 JSON 字符串的控制字节，并限制规范化后的最大大小。
func NormalizeLenientJSONRequestBody(body []byte, maxNormalizedBytes int64) ([]byte, error) {
	if maxNormalizedBytes <= 0 {
		maxNormalizedBytes = maxDecompressedBodySize
	}

	body = trimUTF8BOM(body)
	if len(body) == 0 {
		return body, nil
	}
	if int64(len(body)) > maxNormalizedBytes {
		return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
	}

	var out []byte
	inString := false
	escaped := false
	for i, b := range body {
		if inString && isJSONControlByte(b) {
			if out == nil {
				capHint := len(body) + 6
				if int64(capHint) > maxNormalizedBytes {
					capHint = int(maxNormalizedBytes)
				}
				out = make([]byte, 0, capHint)
				out = append(out, body[:i]...)
			}
			if int64(len(out)+6) > maxNormalizedBytes {
				return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
			}
			out = appendJSONUnicodeEscape(out, b)
			escaped = false
			continue
		}

		switch {
		case escaped:
			escaped = false
		case inString && b == '\\':
			escaped = true
		case b == '"':
			inString = !inString
		}

		if out != nil {
			if int64(len(out)+1) > maxNormalizedBytes {
				return nil, &http.MaxBytesError{Limit: maxNormalizedBytes}
			}
			out = append(out, b)
		}
	}
	if out != nil {
		return validateRoutingRequestBody(out)
	}
	return validateRoutingRequestBody(body)
}

// trimUTF8BOM 移除 JSON 请求体开头的 UTF-8 BOM。
func trimUTF8BOM(body []byte) []byte {
	if len(body) >= jsonUTF8BOMLen && body[0] == 0xef && body[1] == 0xbb && body[2] == 0xbf {
		return body[jsonUTF8BOMLen:]
	}
	return body
}

// isJSONControlByte 判断字节是否需要在 JSON 字符串中转义。
func isJSONControlByte(b byte) bool {
	return b < 0x20 || b == 0x7f
}

// appendJSONUnicodeEscape 把控制字节追加为 \u00xx 形式的 JSON 转义。
func appendJSONUnicodeEscape(dst []byte, b byte) []byte {
	const hex = "0123456789abcdef"
	return append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0x0f])
}
