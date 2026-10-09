package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
)

type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type Request struct {
	Model     string              `json:"model"`
	State     string              `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type Result struct {
	Model  string
	Scores map[string]float64
	Usage  Usage
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Evaluate 只执行单次请求，超时、重试及密钥轮换由调用方管理。
func Evaluate(ctx context.Context, client *http.Client, baseURL, key string, input Request) (*Result, int, error) {
	endpoint, err := url.JoinPath(strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1"), "/v1/systemone")
	if err != nil {
		return nil, 0, errors.New("typesafe invalid endpoint")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, 0, errors.New("typesafe invalid request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("typesafe invalid request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, errors.New("typesafe transport unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 上游错误正文可能回显用户输入或凭据，不写入普通日志。
		return nil, resp.StatusCode, fmt.Errorf("typesafe API status %d", resp.StatusCode)
	}
	var out struct {
		Model   string          `json:"model"`
		Usage   json.RawMessage `json:"usage"`
		Answers map[string]struct {
			Type string   `json:"type"`
			Noul *float64 `json:"noul"`
		} `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || strings.TrimSpace(out.Model) == "" {
		return nil, resp.StatusCode, errors.New("typesafe invalid response")
	}
	// 用量是审核接口的附加信息，兼容数值字符串，不放宽评分本身的校验。
	var rawUsage map[string]json.RawMessage
	_ = json.Unmarshal(out.Usage, &rawUsage)
	result := &Result{Model: out.Model, Usage: Usage{InputTokens: moderationTokenCount(rawUsage["input_tokens"]), OutputTokens: moderationTokenCount(rawUsage["output_tokens"])}, Scores: make(map[string]float64, len(input.Questions))}
	for id := range input.Questions {
		answer, ok := out.Answers[id]
		if !ok || answer.Type != "noul" || answer.Noul == nil || math.IsNaN(*answer.Noul) || math.IsInf(*answer.Noul, 0) || *answer.Noul < 0 || *answer.Noul > 1 {
			return nil, resp.StatusCode, fmt.Errorf("typesafe invalid answer for %s", id)
		}
		result.Scores[id] = *answer.Noul
	}
	return result, resp.StatusCode, nil
}

// 审核附加用量只接受非负有限数值，限界后转换避免溢出。
func moderationTokenCount(raw json.RawMessage) int {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = json.RawMessage(strings.TrimSpace(text))
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return 0
	}
	value, err := number.Float64()
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	return int(math.Round(math.Min(value, 1<<40)))
}
