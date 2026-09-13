package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

// ReplaceModelMetadata 只替换常见协议中的模型元数据字段，不触碰正文内容。
func ReplaceModelMetadata(data []byte, fromModel, toModel string) []byte {
	fromModel = strings.TrimSpace(fromModel)
	toModel = strings.TrimSpace(toModel)
	if fromModel == "" || toModel == "" || fromModel == toModel {
		return data
	}
	fromValue, _ := json.Marshal(fromModel)
	toValue, _ := json.Marshal(toModel)
	fromGeminiName, _ := json.Marshal("models/" + fromModel)
	toGeminiName, _ := json.Marshal("models/" + toModel)
	patterns := [][2][]byte{
		{append([]byte(`"model":`), fromValue...), append([]byte(`"model":`), toValue...)},
		{append([]byte(`"model": `), fromValue...), append([]byte(`"model": `), toValue...)},
		{append([]byte(`"modelVersion":`), fromValue...), append([]byte(`"modelVersion":`), toValue...)},
		{append([]byte(`"modelVersion": `), fromValue...), append([]byte(`"modelVersion": `), toValue...)},
		{append([]byte(`"model_version":`), fromValue...), append([]byte(`"model_version":`), toValue...)},
		{append([]byte(`"model_version": `), fromValue...), append([]byte(`"model_version": `), toValue...)},
		{append([]byte(`"id":`), fromValue...), append([]byte(`"id":`), toValue...)},
		{append([]byte(`"id": `), fromValue...), append([]byte(`"id": `), toValue...)},
		{append([]byte(`"name":`), fromGeminiName...), append([]byte(`"name":`), toGeminiName...)},
		{append([]byte(`"name": `), fromGeminiName...), append([]byte(`"name": `), toGeminiName...)},
	}
	rewritten := data
	for _, pattern := range patterns {
		rewritten = bytes.ReplaceAll(rewritten, pattern[0], pattern[1])
	}
	return rewritten
}

// RestoreAPIKeyModelResponse 按请求追踪恢复 Key 重定向产生的内部模型名。
func RestoreAPIKeyModelResponse(ctx context.Context, data []byte) []byte {
	trace, ok := APIKeyModelRedirectTraceFromContext(ctx)
	if !ok || strings.TrimSpace(trace.ClientModel) == "" {
		return data
	}
	trace.RegisterResponsePayload(data)
	rewritten := data
	for _, model := range trace.ResponseModels() {
		rewritten = ReplaceModelMetadata(rewritten, model, trace.ClientModel)
	}
	return rewritten
}

// RegisterResponsePayload 从完整 JSON 或 SSE data 事件中登记实际上游模型元数据。
func (t *APIKeyModelRedirectTrace) RegisterResponsePayload(data []byte) {
	if t == nil || len(data) == 0 {
		return
	}
	for _, model := range responseMetadataModels(data) {
		t.addResponseModel(model)
	}
}

// responseMetadataModels 只读取协议模型字段，不扫描正文或工具参数中的同名文本。
func responseMetadataModels(data []byte) []string {
	payloads := make([][]byte, 0, 4)
	trimmed := bytes.TrimSpace(data)
	if json.Valid(trimmed) {
		payloads = append(payloads, trimmed)
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || !json.Valid(payload) {
			continue
		}
		payloads = append(payloads, payload)
	}

	paths := []string{"model", "modelVersion", "model_version", "response.model", "session.model"}
	models := make([]string, 0, len(payloads))
	seen := make(map[string]struct{})
	appendModel := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		if _, exists := seen[model]; exists {
			return
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	for _, payload := range payloads {
		for _, path := range paths {
			value := gjson.GetBytes(payload, path)
			if value.Type == gjson.String {
				appendModel(value.String())
			}
		}
		for _, path := range []string{"name", "response.name"} {
			name := strings.TrimSpace(gjson.GetBytes(payload, path).String())
			if strings.HasPrefix(name, "models/") {
				appendModel(strings.TrimPrefix(name, "models/"))
			}
		}
	}
	return models
}
