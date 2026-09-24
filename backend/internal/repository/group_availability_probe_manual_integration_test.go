//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// newManualProbeGroup 只在集成测试容器内建立独立分组，不依赖已有业务数据。
func newManualProbeGroup(t *testing.T, enabled bool) int64 {
	t.Helper()
	var id int64
	err := integrationDB.QueryRowContext(context.Background(), `INSERT INTO groups
		(name, platform, status, availability_probe_config) VALUES ($1,'kimi','active',
		jsonb_build_object('enabled',$2::boolean,'model_id','kimi-k3','prompt','hi','protocol','chat_completions','timeout_seconds',5)) RETURNING id`,
		"manual-probe-"+uuid.NewString(), enabled).Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM scheduler_outbox WHERE group_id=$1`, id)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id=$1`, id)
	})
	return id
}

// manualProbeConfig 使用可执行的独立配置，便于验证原子提交的是本次请求值。
func manualProbeConfig() service.GroupAvailabilityProbeConfig {
	return service.GroupAvailabilityProbeConfig{Enabled: true, Protocol: "chat_completions", ModelID: "kimi-k3", Prompt: "hi", TimeoutSeconds: 5, IntervalMinutes: 30}
}

func TestGroupAvailabilityProbeManualPostgres_ClaimAndHistory(t *testing.T) {
	ctx := context.Background()
	id := newManualProbeGroup(t, true)
	repo := NewGroupAvailabilityProbeRepository(integrationDB)
	now := time.Now().UTC().Truncate(time.Microsecond)
	// 即使周期任务尚未到期，立即测试仍可领取；领取后 cron 与重复点击均被阻止。
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO group_availability_probe_states(group_id,next_run_at) VALUES($1,$2)`, id, now.Add(time.Hour))
	require.NoError(t, err)
	claim, err := repo.ClaimGroup(ctx, id, manualProbeConfig(), now, now.Add(time.Minute), "manual")
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, "chat_completions", claim.Config.Protocol)
	duplicateConfig := manualProbeConfig()
	duplicateConfig.Protocol = "responses"
	duplicate, err := repo.ClaimGroup(ctx, id, duplicateConfig, now, now.Add(time.Minute), "manual-second")
	require.NoError(t, err)
	require.Nil(t, duplicate)
	var storedProtocol string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT availability_probe_config->>'protocol' FROM groups WHERE id=$1`, id).Scan(&storedProtocol))
	require.Equal(t, "chat_completions", storedProtocol, "忙碌请求不能覆盖当前测试配置")
	_, err = integrationDB.ExecContext(ctx, `UPDATE group_availability_probe_states SET next_run_at=$2 WHERE group_id=$1`, id, now.Add(-time.Minute))
	require.NoError(t, err)
	due, err := repo.ClaimDue(ctx, now, now.Add(time.Minute), "cron", 5)
	require.NoError(t, err)
	for _, group := range due {
		require.NotEqual(t, id, group.GroupID)
	}
	result := &service.GroupAvailabilityProbeResult{GroupID: id, ModelID: "kimi-k3", Status: "success", Success: true, LatencyMs: 17, StartedAt: now, FinishedAt: now}
	require.NoError(t, repo.SaveResultAndScheduleNext(ctx, result, now.Add(time.Hour)))
	summary, err := repo.GetSummaryByGroupIDs(ctx, []int64{id}, 1, 5, "UTC", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(1), summary[id].SuccessCount)
	require.Equal(t, int64(1), summary[id].TotalCount)
	require.Equal(t, "success", summary[id].LastStatus)
	// 下一次手动失败也必须进入历史，而不是只更新临时弹窗。
	claim, err = repo.ClaimGroup(ctx, id, manualProbeConfig(), now.Add(time.Second), now.Add(time.Minute), "manual-again")
	require.NoError(t, err)
	require.NotNil(t, claim)
	result.Success = false
	result.Status = "failed"
	result.ErrorMessage = "upstream rejected protocol"
	result.StartedAt = now.Add(time.Second)
	result.FinishedAt = result.StartedAt
	require.NoError(t, repo.SaveResultAndScheduleNext(ctx, result, now.Add(time.Hour)))
	summary, err = repo.GetSummaryByGroupIDs(ctx, []int64{id}, 1, 5, "UTC", now.Add(2*time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(2), summary[id].TotalCount)
	require.Equal(t, int64(1), summary[id].SuccessCount)
	require.Equal(t, "failed", summary[id].LastStatus)
}

func TestGroupAvailabilityProbeManualPostgres_ConcurrentClaim(t *testing.T) {
	id := newManualProbeGroup(t, true)
	repo := NewGroupAvailabilityProbeRepository(integrationDB)
	now := time.Now()
	var wg sync.WaitGroup
	results := make(chan *service.GroupAvailabilityProbeDueGroup, 12)
	errors := make(chan error, 12)
	for i := 0; i < 12; i++ {
		config := manualProbeConfig()
		config.Prompt = fmt.Sprintf("probe request %d", i)
		if i%2 == 0 {
			config.Protocol = "responses"
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := repo.ClaimGroup(context.Background(), id, config, now, now.Add(time.Minute), uuid.NewString())
			errors <- err
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	winners := 0
	for claimed := range results {
		if claimed != nil {
			winners++
			var protocol, prompt string
			require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT availability_probe_config->>'protocol', availability_probe_config->>'prompt' FROM groups WHERE id=$1`, id).Scan(&protocol, &prompt))
			require.Equal(t, claimed.Config.Protocol, protocol)
			require.Equal(t, claimed.Config.Prompt, prompt)
		}
	}
	require.Equal(t, 1, winners)
}

func TestGroupAvailabilityProbeManualPostgres_EnableAndCronLease(t *testing.T) {
	ctx := context.Background()
	repo := NewGroupAvailabilityProbeRepository(integrationDB)
	now := time.Now()
	disabled := newManualProbeGroup(t, false)
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET rate_multiplier=2.5, description='preserve other settings' WHERE id=$1`, disabled)
	require.NoError(t, err)
	claim, err := repo.ClaimGroup(ctx, disabled, manualProbeConfig(), now, now.Add(time.Minute), "manual")
	require.NoError(t, err)
	require.NotNil(t, claim, "首次开启探测可直接点击立即测试")
	var enabled bool
	var rate float64
	var description string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (availability_probe_config->>'enabled')::boolean, rate_multiplier, description FROM groups WHERE id=$1`, disabled).Scan(&enabled, &rate, &description))
	require.True(t, enabled)
	require.Equal(t, 2.5, rate)
	require.Equal(t, "preserve other settings", description)
	var events int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduler_outbox WHERE group_id=$1 AND event_type=$2`, disabled, service.SchedulerOutboxEventGroupChanged).Scan(&events))
	require.Equal(t, 1, events)
	id := newManualProbeGroup(t, true)
	due, err := repo.ClaimDue(ctx, now, now.Add(time.Minute), "cron", 5)
	require.NoError(t, err)
	found := false
	for _, group := range due {
		if group.GroupID == id {
			found = true
		}
	}
	require.True(t, found)
	claim, err = repo.ClaimGroup(ctx, id, manualProbeConfig(), now, now.Add(time.Minute), "manual")
	require.NoError(t, err)
	require.Nil(t, claim)
}

// 定时清理不得删除仍持有租约的状态，否则关闭后重新开启会重复发送请求。
func TestGroupAvailabilityProbeManualPostgres_CleanupKeepsRunningLease(t *testing.T) {
	ctx := context.Background()
	id := newManualProbeGroup(t, false)
	repo := NewGroupAvailabilityProbeRepository(integrationDB)
	now := time.Now()
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO group_availability_probe_states(group_id,locked_until,locked_by) VALUES($1,$2,'running')`, id, now.Add(time.Minute))
	require.NoError(t, err)
	_, err = repo.ClaimDue(ctx, now, now.Add(time.Minute), "cron", 5)
	require.NoError(t, err)
	claim, err := repo.ClaimGroup(ctx, id, manualProbeConfig(), now, now.Add(time.Minute), "manual")
	require.NoError(t, err)
	require.Nil(t, claim)
	var enabled bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT (availability_probe_config->>'enabled')::boolean FROM groups WHERE id=$1`, id).Scan(&enabled))
	require.False(t, enabled)
}
