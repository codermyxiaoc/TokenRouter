package repository

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 摘要大小不能随作品增长；私有恢复凭证不属于任何可展示 JSON 列。
func TestIntelligenceRunSummaryPayloadSplit(t *testing.T) {
	run := &service.IntelligenceRun{ID: "iq-test", ConfigID: 1, GroupID: 2, Model: "test-model", Benchmark: "drawing", Status: "completed", Verdict: "passed", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), HTML: "<svg>" + strings.Repeat("画", 200000) + "</svg>", Question: "full question", Answer: strings.Repeat("full answer", 20000), AssessmentReason: "full assessment", RemoteID: "private-remote-token", LeaseToken: "private-lease", HasDetail: true}
	record, payload, err := marshalIntelligenceRun(run)
	require.NoError(t, err)
	require.Less(t, len(record), 2048)
	require.Greater(t, len(payload), 500000)
	require.NotContains(t, string(record), "private-")
	require.NotContains(t, string(payload), "private-")
	var summary, detail map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(record, &summary))
	require.NoError(t, json.Unmarshal(payload, &detail))
	for _, field := range []string{"html", "question", "answer", "assessment_reason"} {
		require.NotContains(t, summary, field)
		require.Contains(t, detail, field)
	}
	require.Len(t, detail, 4)
	require.JSONEq(t, `true`, string(summary["has_artifact"]))
	// 模拟 PostgreSQL JSONB 合并，验证详情与恢复路径取得完整正文。
	for field, value := range detail {
		summary[field] = value
	}
	merged, err := json.Marshal(summary)
	require.NoError(t, err)
	var restored service.IntelligenceRun
	require.NoError(t, json.Unmarshal(merged, &restored))
	require.Equal(t, run.HTML, restored.HTML)
	require.Equal(t, run.Question, restored.Question)
	require.Equal(t, run.Answer, restored.Answer)
	require.Equal(t, run.AssessmentReason, restored.AssessmentReason)
	require.Equal(t, run.Model, restored.Model)
	require.Empty(t, restored.RemoteID)
	require.Empty(t, restored.LeaseToken)
	require.False(t, run.HasArtifact, "存储投影不能反向修改调用方对象")
}

func TestIntelligenceRunEmptyPayloadAndMissingRun(t *testing.T) {
	record, payload, err := marshalIntelligenceRun(&service.IntelligenceRun{ID: "iq-empty", HasArtifact: true})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(payload))
	var summary service.IntelligenceRun
	require.NoError(t, json.Unmarshal(record, &summary))
	require.False(t, summary.HasArtifact)
	_, _, err = marshalIntelligenceRun(nil)
	require.ErrorIs(t, err, service.ErrIntelligenceNotFound)
}
