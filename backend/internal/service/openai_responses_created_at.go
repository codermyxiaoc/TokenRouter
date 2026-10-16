package service

import (
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 同一 Responses 流仅为缺失或 null 的 created_at 补值，保持上游已有时间和正文不变。
type responsesCreatedAtStamper struct {
	createdAt int64
}

func (s *responsesCreatedAtStamper) learn(value gjson.Result) {
	if s.createdAt == 0 && value.Type == gjson.Number && value.Int() > 0 {
		s.createdAt = value.Int()
	}
}

// 首次需要时固定本轮时间，后续缺失值沿用。
func (s *responsesCreatedAtStamper) Value() int64 {
	if s.createdAt <= 0 {
		s.createdAt = time.Now().Unix()
	}
	return s.createdAt
}

// 透传仅观察时间，不能为上游原始事件新增字段。
func (s *responsesCreatedAtStamper) Observe(payload []byte, eventType string) {
	if !strings.HasPrefix(strings.TrimSpace(eventType), "response.") {
		return
	}
	s.learn(gjson.GetBytes(payload, "response.created_at"))
}

// 生命周期事件的 response 对象缺失时间时补齐，其它类型或已有值保真。
func (s *responsesCreatedAtStamper) Stamp(payload []byte, eventType string) ([]byte, bool) {
	if !strings.HasPrefix(strings.TrimSpace(eventType), "response.") {
		return payload, false
	}
	response := gjson.GetBytes(payload, "response")
	if !response.IsObject() {
		return payload, false
	}
	createdAt := response.Get("created_at")
	if createdAt.Exists() && createdAt.Type != gjson.Null {
		s.learn(createdAt)
		return payload, false
	}
	stamped, err := sjson.SetBytes(payload, "response.created_at", s.Value())
	if err != nil {
		return payload, false
	}
	return stamped, true
}

// 非流式 Response 对象缺少时间时补齐，其它 JSON 不处理。
func ensureOpenAIResponseObjectCreatedAt(body []byte) []byte {
	if gjson.GetBytes(body, "object").String() != "response" {
		return body
	}
	createdAt := gjson.GetBytes(body, "created_at")
	if createdAt.Exists() && createdAt.Type != gjson.Null {
		return body
	}
	stamped, err := sjson.SetBytes(body, "created_at", time.Now().Unix())
	if err != nil {
		return body
	}
	return stamped
}

// 合成失败帧复用被抑制的裸错误序号；未知时使用零。
func openAIResponsesFailedSequenceNumber(source []byte) int64 {
	if seq := gjson.GetBytes(source, "sequence_number"); seq.Type == gjson.Number && seq.Int() >= 0 {
		return seq.Int()
	}
	return 0
}
