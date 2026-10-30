package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Grok 后台目录同步的解析与来源判断，参照上游 Sub2API v0.2.19 的 grok_model_catalog.go 精简移植：
// 只保留严格解析、来源指纹与新鲜度判断；本站快照没有读取方参与路由或计费，不引入 CAS 与尝试历史。
// @project-doc docs/interfaces/grok_upstream.md#grok_observed_models

var (
	errGrokCatalogInvalidResponse  = errors.New("grok catalog: invalid response")
	errGrokCatalogEmpty            = errors.New("grok catalog: empty catalog")
	errGrokCatalogInvalidModelsURL = errors.New("grok catalog: invalid models url")
)

// GrokCatalogScope 标识一次目录来自哪个端点与账号；端点或账号类型变化后旧快照不再视为新鲜。
type GrokCatalogScope struct {
	AccountID   int64  `json:"account_id"`
	AccountType string `json:"account_type"`
	Endpoint    string `json:"endpoint"`
	Fingerprint string `json:"fingerprint"`
}

// NewGrokCatalogScope 用规范化后的目录地址、账号 ID 与类型计算指纹；指纹不包含凭据。
func NewGrokCatalogScope(accountID int64, accountType, modelsURL string) (*GrokCatalogScope, error) {
	parsed, err := url.Parse(modelsURL)
	if err != nil {
		return nil, errGrokCatalogInvalidModelsURL
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Hostname() == "" || (scheme != "http" && scheme != "https") {
		return nil, errGrokCatalogInvalidModelsURL
	}
	endpoint := scheme + "://" + strings.ToLower(parsed.Host) + strings.TrimRight(parsed.EscapedPath(), "/")
	canonical := endpoint
	if parsed.RawQuery != "" {
		canonical += "?" + parsed.RawQuery
	}
	sum := sha256.Sum256([]byte(canonical + "\n" + strconv.FormatInt(accountID, 10) + "\n" + accountType))
	return &GrokCatalogScope{AccountID: accountID, AccountType: accountType, Endpoint: endpoint, Fingerprint: hex.EncodeToString(sum[:])}, nil
}

// grokObservedSnapshotFresh 只有同一来源、时间合法且不晚于当前时间的快照才在 TTL 内跳过同步；
// 未来时间、非法时间、缺少来源的旧快照都会重新拉取。
func grokObservedSnapshotFresh(snap *grokObservedModelsSnapshot, scope *GrokCatalogScope, now time.Time) bool {
	if snap == nil || scope == nil || strings.TrimSpace(scope.Fingerprint) == "" || len(snap.Models) == 0 {
		return false
	}
	if snap.Scope == nil || snap.Scope.Fingerprint != scope.Fingerprint {
		return false
	}
	fetched, ok := parseGrokCatalogTimestamp(snap.FetchedAt)
	if !ok || fetched.After(now) {
		return false
	}
	return now.Sub(fetched) < grokObservedModelsTTL
}

func parseGrokCatalogTimestamp(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// parseGrokCatalogModelIDs 严格解析目录：只接受顶层数组或含 data/models 数组的对象；HTTP 200 但带 error 的响应、
// 非数组、元素不是字符串或对象都视为无效，不会把整段原始 JSON 当作模型 ID 保存。ID 选取规则与手动同步一致。
func parseGrokCatalogModelIDs(body []byte) ([]string, error) {
	raw := bytes.TrimSpace(body)
	if len(raw) == 0 || !json.Valid(raw) || bytes.Equal(raw, []byte("null")) {
		return nil, errGrokCatalogInvalidResponse
	}
	var entries []json.RawMessage
	switch raw[0] {
	case '[':
		items, err := decodeGrokCatalogArray(raw)
		if err != nil {
			return nil, err
		}
		entries = items
	case '{':
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, errGrokCatalogInvalidResponse
		}
		if errRaw, ok := obj["error"]; ok && !bytes.Equal(bytes.TrimSpace(errRaw), []byte("null")) {
			return nil, errGrokCatalogInvalidResponse
		}
		sawCatalog := false
		for _, key := range []string{"data", "models"} {
			value, ok := obj[key]
			if !ok {
				continue
			}
			sawCatalog = true
			items, err := decodeGrokCatalogArray(value)
			if err != nil {
				return nil, err
			}
			entries = append(entries, items...)
		}
		if !sawCatalog {
			return nil, errGrokCatalogInvalidResponse
		}
	default:
		return nil, errGrokCatalogInvalidResponse
	}
	models := make([]string, 0, len(entries))
	for _, entry := range entries {
		entry = bytes.TrimSpace(entry)
		if len(entry) == 0 {
			return nil, errGrokCatalogInvalidResponse
		}
		switch entry[0] {
		case '"':
			var id string
			if err := json.Unmarshal(entry, &id); err != nil {
				return nil, errGrokCatalogInvalidResponse
			}
			models = append(models, id)
		case '{':
			var item upstreamModelEntry
			if err := json.Unmarshal(entry, &item); err != nil {
				return nil, errGrokCatalogInvalidResponse
			}
			models = append(models, grokUpstreamModelEntryID(item))
		default:
			return nil, errGrokCatalogInvalidResponse
		}
	}
	ids := dedupeAndSortModelIDs(models)
	if len(ids) == 0 {
		return nil, errGrokCatalogEmpty
	}
	return ids, nil
}

func decodeGrokCatalogArray(raw json.RawMessage) ([]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, errGrokCatalogInvalidResponse
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errGrokCatalogInvalidResponse
	}
	return items, nil
}

// grokCatalogSyncError 携带失败分类与状态码；后台日志只输出这两项（见 grokObservedModelsSyncLogFields），
// 底层原因仅供调用方用 errors.Is/As 判断，不进入日志，避免传输层细节或凭据外泄。
type grokCatalogSyncError struct {
	Kind       string
	StatusCode int
	Err        error
}

func (e *grokCatalogSyncError) Error() string {
	msg := "grok catalog sync failed: " + e.Kind
	if e.StatusCode > 0 {
		msg += " (HTTP " + strconv.Itoa(e.StatusCode) + ")"
	}
	// 只有配置错误（URL 策略等本地校验）附带原因；传输错误可能含上游细节，不拼入文本。
	if e.Kind == "configuration" && e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *grokCatalogSyncError) Unwrap() error { return e.Err }

func grokObservedModelsSyncLogFields(err error) (string, int) {
	var syncErr *grokCatalogSyncError
	if errors.As(err, &syncErr) {
		return syncErr.Kind, syncErr.StatusCode
	}
	return "internal", 0
}
