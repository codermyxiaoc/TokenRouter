package service

import (
	"context"
	"errors"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 手动执行桩只替换数据库领取和调度查询，验证真实 runner 的单次执行及持久化流程。
type manualProbeRepoStub struct {
	groupAvailabilityProbeRunnerRepoStub
	due     *GroupAvailabilityProbeDueGroup
	onClaim func()
	claims  int
	saved   []*GroupAvailabilityProbeResult
	saveErr error
	next    time.Time
}

func (r *manualProbeRepoStub) ClaimGroup(_ context.Context, _ int64, config GroupAvailabilityProbeConfig, _ time.Time, _ time.Time, _ string) (*GroupAvailabilityProbeDueGroup, error) {
	r.claims++
	if r.due != nil {
		r.due.Config = config
	}
	if r.onClaim != nil {
		r.onClaim()
	}
	return r.due, nil
}

func (r *manualProbeRepoStub) SaveResultAndScheduleNext(ctx context.Context, result *GroupAvailabilityProbeResult, next time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.saved = append(r.saved, result)
	r.next = next
	return r.saveErr
}

type manualProbeGroupRepoStub struct {
	GroupRepository
	calls int
}

func (r *manualProbeGroupRepoStub) GetByID(ctx context.Context, _ int64) (*Group, error) {
	r.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("test group has no eligible accounts")
}

func (r *manualProbeGroupRepoStub) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

func TestGroupAvailabilityProbeManualOnceRecordsFailureWithoutRetries(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(map[bool]string{false: "connected", true: "client_disconnected"}[disconnect], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			retries := 10
			repo := &manualProbeRepoStub{due: &GroupAvailabilityProbeDueGroup{GroupID: 42, Platform: PlatformKimi, Config: GroupAvailabilityProbeConfig{
				Enabled: true, Protocol: APIProtocolChatCompletions, ModelID: "kimi-k3", Prompt: "hi", IntervalMinutes: 30, TimeoutSeconds: 5, MaxRetries: &retries,
			}}}
			if disconnect {
				repo.onClaim = cancel
			}
			groups := &manualProbeGroupRepoStub{}
			runner := NewGroupAvailabilityProbeRunnerService(repo, &AccountTestService{}, &GatewayService{groupRepo: groups}, nil, nil, nil)
			before := time.Now()
			result, err := runner.RunOnce(ctx, 42, repo.due.Config)
			require.NoError(t, err)
			require.False(t, result.Success)
			require.Equal(t, 1, groups.calls, "手动测试不能使用周期探测的十次重试")
			require.Equal(t, APIProtocolChatCompletions, result.Protocol)
			require.Contains(t, result.ErrorMessage, "no eligible accounts")
			require.Len(t, repo.saved, 1)
			require.Same(t, result, repo.saved[0])
			require.True(t, repo.next.After(before.Add(29*time.Minute)))
			require.Zero(t, runner.manualRunning)
		})
	}
}

func TestGroupAvailabilityProbeManualBusyDoesNotCreateObservation(t *testing.T) {
	repo := &manualProbeRepoStub{}
	runner := NewGroupAvailabilityProbeRunnerService(repo, &AccountTestService{}, nil, nil, nil, nil)
	config := GroupAvailabilityProbeConfig{Enabled: true, ModelID: "kimi-k3", Prompt: "hi"}
	_, err := runner.RunOnce(context.Background(), 42, config)
	require.Equal(t, 409, infraerrors.Code(err))
	require.Equal(t, "GROUP_AVAILABILITY_PROBE_BUSY", infraerrors.Reason(err))
	require.Empty(t, repo.saved)
	require.Zero(t, runner.manualRunning)
	runner.manualRunning = groupAvailabilityProbeDefaultMaxWorkers
	_, err = runner.RunOnce(context.Background(), 43, config)
	require.Equal(t, 409, infraerrors.Code(err))
	require.Equal(t, 1, repo.claims)
}

func TestGroupAvailabilityProbeManualSaveErrorIsReturned(t *testing.T) {
	repo := &manualProbeRepoStub{due: &GroupAvailabilityProbeDueGroup{GroupID: 42, Platform: PlatformKimi, Config: GroupAvailabilityProbeConfig{
		Enabled: true, ModelID: "kimi-k3", Prompt: "hi", TimeoutSeconds: 5,
	}}, saveErr: errors.New("database write failed")}
	runner := NewGroupAvailabilityProbeRunnerService(repo, &AccountTestService{}, nil, nil, nil, nil)
	result, err := runner.RunOnce(context.Background(), 42, repo.due.Config)
	require.Nil(t, result)
	require.ErrorContains(t, err, "save manual availability probe result")
}

// 无效配置必须在触及数据库或上游前拒绝，避免修改已生效的探测设置。
func TestGroupAvailabilityProbeManualRejectsInvalidConfigBeforeClaim(t *testing.T) {
	for _, config := range []GroupAvailabilityProbeConfig{
		{},
		{Enabled: true, Prompt: "hi"},
		{Enabled: true, ModelID: "kimi-k3", Prompt: "hi", Protocol: "invalid"},
		{Enabled: true, ModelID: "kimi-k3", Prompt: "hi", TimeoutSeconds: 121},
	} {
		repo := &manualProbeRepoStub{}
		runner := NewGroupAvailabilityProbeRunnerService(repo, &AccountTestService{}, nil, nil, nil, nil)
		_, err := runner.RunOnce(context.Background(), 42, config)
		require.Equal(t, 400, infraerrors.Code(err))
		require.Zero(t, repo.claims)
		require.Empty(t, repo.saved)
	}
}
