package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 测试替身只记录账务命令；资金事务与并发幂等另由仓储集成测试验证。
type reconciliationUnitRepo struct {
	MediaTaskRepository
	task       MediaTask
	review     *MediaTaskBillingReview
	reviewErr  error
	getErr     error
	commands   []MediaTaskSettlementCommand
	readActors []MediaTaskActor
	cutoffs    []time.Time
	limits     []int
	ids        []int64
	scanErr    error
	resolve    func(MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error)
}

func (r *reconciliationUnitRepo) Get(_ context.Context, actor MediaTaskActor, _ int64) (*MediaTask, error) {
	r.readActors = append(r.readActors, actor)
	copy := r.task
	return &copy, r.getErr
}
func (r *reconciliationUnitRepo) List(_ context.Context, actor MediaTaskActor, _ MediaTaskFilter) (*MediaTaskList, error) {
	r.readActors = append(r.readActors, actor)
	return &MediaTaskList{Items: []MediaTask{r.task}}, nil
}
func (r *reconciliationUnitRepo) GetMediaTaskBillingReview(context.Context, *MediaTask) (*MediaTaskBillingReview, error) {
	if r.review == nil {
		return nil, r.reviewErr
	}
	copy := *r.review
	return &copy, r.reviewErr
}
func (r *reconciliationUnitRepo) ResolveMediaTaskBilling(_ context.Context, command MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error) {
	r.commands = append(r.commands, command)
	if r.resolve != nil {
		return r.resolve(command)
	}
	return nil, nil
}
func (r *reconciliationUnitRepo) ListExpiredMediaTaskBilling(_ context.Context, cutoff time.Time, limit int) ([]int64, error) {
	r.cutoffs, r.limits = append(r.cutoffs, cutoff), append(r.limits, limit)
	return r.ids, r.scanErr
}

type reconciliationUnitSettings struct {
	timeout time.Duration
	err     error
	reads   int
}

func (s *reconciliationUnitSettings) GetMediaTaskReconciliationTimeout(context.Context) (time.Duration, error) {
	s.reads++
	return s.timeout, s.err
}

func TestMediaTaskReconciliationSettlementValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		actor  MediaTaskActor
		id     int64
		action string
		reason string
		code   int
	}{
		{"个人入口管理员也不能写", MediaTaskActor{UserID: 7}, 1, "charge", "核对完成", http.StatusForbidden},
		{"缺少管理员身份", MediaTaskActor{IsAdmin: true}, 1, "release", "核对完成", http.StatusForbidden},
		{"非法任务", MediaTaskActor{UserID: 7, IsAdmin: true}, 0, "charge", "核对完成", http.StatusBadRequest},
		{"不接受自动动作", MediaTaskActor{UserID: 7, IsAdmin: true}, 1, "expire", "核对完成", http.StatusBadRequest},
		{"缺少动作", MediaTaskActor{UserID: 7, IsAdmin: true}, 1, "", "核对完成", http.StatusBadRequest},
		{"缺少理由", MediaTaskActor{UserID: 7, IsAdmin: true}, 1, "release", " \t\n ", http.StatusBadRequest},
		{"理由按字符限制", MediaTaskActor{UserID: 7, IsAdmin: true}, 1, "charge", strings.Repeat("字", 501), http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &reconciliationUnitRepo{}
			result, err := NewMediaTaskService(repo).SettleBilling(context.Background(), test.actor, test.id, test.action, test.reason)
			require.Equal(t, test.code, infraerrors.Code(err))
			require.Nil(t, result)
			require.Empty(t, repo.commands)
			require.Empty(t, repo.readActors)
		})
	}
}

func TestMediaTaskReconciliationSettlementReadsCommittedStateOnReplay(t *testing.T) {
	for _, action := range []string{"charge", "release"} {
		t.Run(action, func(t *testing.T) {
			repo := &reconciliationUnitRepo{task: MediaTask{ID: 3, UserID: 99, Source: "async_image", Status: "failed"}}
			repo.resolve = func(command MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error) {
				status := "released"
				if command.Action == "charge" {
					status = "charged"
				}
				repo.review = &MediaTaskBillingReview{Status: status, Reason: command.Reason, ActorID: &command.ActorID}
				return &MediaTaskSettlementResult{UserID: 99, ActorUserID: 100, APIKeyID: 9}, nil
			}
			s := NewMediaTaskService(repo)
			actor := MediaTaskActor{UserID: 7, IsAdmin: true}
			for range 2 {
				updated, err := s.SettleBilling(context.Background(), actor, 3, action, "  "+strings.Repeat("字", 500)+"  ")
				require.NoError(t, err)
				require.EqualValues(t, 99, updated.UserID, "处理管理员不能成为付款人")
				require.Equal(t, strings.Repeat("字", 500), updated.BillingReview.Reason)
				require.EqualValues(t, 7, *updated.BillingReview.ActorID)
			}
			require.Len(t, repo.commands, 2)
			require.Equal(t, repo.commands[0], repo.commands[1])
			require.Nil(t, repo.commands[0].FailedBefore, "人工决策不能伪装维护器的过期条件")
			require.Equal(t, []MediaTaskActor{actor, actor}, repo.readActors, "重复成功后仍读取最新已提交状态")
		})
	}
}

func TestMediaTaskReconciliationConflictDoesNotReadOrRetry(t *testing.T) {
	repo := &reconciliationUnitRepo{resolve: func(MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error) {
		return nil, ErrMediaTaskSettlementConflict
	}}
	result, err := NewMediaTaskService(repo).SettleBilling(context.Background(), MediaTaskActor{UserID: 7, IsAdmin: true}, 1, "charge", "核对完成")
	require.ErrorIs(t, err, ErrMediaTaskSettlementConflict)
	require.Nil(t, result)
	require.Len(t, repo.commands, 1)
	require.Empty(t, repo.readActors)
}

func TestMediaTaskReconciliationWorkerSkipsUnreadableSetting(t *testing.T) {
	settingErr := errors.New("settings unavailable")
	repo := &reconciliationUnitRepo{ids: []int64{1}}
	s := NewMediaTaskService(repo)
	settings := &reconciliationUnitSettings{timeout: time.Hour, err: settingErr}
	s.reconciliationSettings = settings
	require.ErrorIs(t, s.ReconcileFailedBilling(context.Background()), settingErr)
	require.Equal(t, 1, settings.reads)
	require.Empty(t, repo.cutoffs)
	require.Empty(t, repo.commands)
}

func TestMediaTaskReconciliationWorkerExpiresWithCurrentCutoffWithoutRetry(t *testing.T) {
	repo := &reconciliationUnitRepo{ids: []int64{10, 20, 30}}
	repo.resolve = func(command MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error) {
		if command.TaskID == 20 {
			return nil, ErrMediaTaskSettlementConflict
		}
		return nil, nil
	}
	s := NewMediaTaskService(repo)
	settings := &reconciliationUnitSettings{timeout: 90 * time.Minute}
	s.reconciliationSettings = settings
	before := time.Now().UTC().Add(-settings.timeout)
	require.NoError(t, s.ReconcileFailedBilling(context.Background()))
	after := time.Now().UTC().Add(-settings.timeout)
	require.Len(t, repo.cutoffs, 1)
	require.False(t, repo.cutoffs[0].Before(before))
	require.False(t, repo.cutoffs[0].After(after))
	require.Equal(t, []int{100}, repo.limits)
	require.Len(t, repo.commands, 3, "冲突不在同轮重复尝试")
	for i, command := range repo.commands {
		require.Equal(t, repo.ids[i], command.TaskID)
		require.Equal(t, "expire", command.Action)
		require.Zero(t, command.ActorID)
		require.NotNil(t, command.FailedBefore)
		require.Equal(t, repo.cutoffs[0], *command.FailedBefore)
	}
	settings.timeout = 3 * time.Hour
	require.NoError(t, s.ReconcileFailedBilling(context.Background()))
	require.Equal(t, 2, settings.reads, "下一轮重新读取配置")
	require.WithinDuration(t, time.Now().UTC().Add(-3*time.Hour), repo.cutoffs[1], time.Second)
}

func TestMediaTaskReconciliationWorkerStopsOnCancellationAndReportsFailures(t *testing.T) {
	t.Run("扫描失败不进行资金动作", func(t *testing.T) {
		scanErr := errors.New("scan failed")
		repo := &reconciliationUnitRepo{scanErr: scanErr, ids: []int64{1}}
		require.ErrorIs(t, NewMediaTaskService(repo).ReconcileFailedBilling(context.Background()), scanErr)
		require.Empty(t, repo.commands)
	})
	t.Run("单笔失败不妨碍其它到期释放", func(t *testing.T) {
		settlementErr := errors.New("transaction failed")
		repo := &reconciliationUnitRepo{ids: []int64{1, 2}, resolve: func(command MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error) {
			if command.TaskID == 1 {
				return nil, settlementErr
			}
			return nil, nil
		}}
		require.ErrorIs(t, NewMediaTaskService(repo).ReconcileFailedBilling(context.Background()), settlementErr)
		require.Len(t, repo.commands, 2)
	})
	t.Run("取消后不领取下一笔", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		repo := &reconciliationUnitRepo{ids: []int64{1, 2}, resolve: func(MediaTaskSettlementCommand) (*MediaTaskSettlementResult, error) { cancel(); return nil, nil }}
		require.ErrorIs(t, NewMediaTaskService(repo).ReconcileFailedBilling(ctx), context.Canceled)
		require.Len(t, repo.commands, 1)
	})
}

func TestMediaTaskReconciliationSummaryRedactsPersonalEntryAndShowsDeadline(t *testing.T) {
	completed := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	actorID := int64(7)
	for _, admin := range []bool{false, true} {
		repo := &reconciliationUnitRepo{task: MediaTask{ID: 1, Source: "async_image", Status: "failed", CompletedAt: &completed, UpdatedAt: completed.Add(time.Minute)},
			review: &MediaTaskBillingReview{Status: "pending", HeldBalanceAmount: .3, HeldSubscriptionAmount: .2, CanCharge: true, CanRelease: true, Reason: "内部核对记录", ActorID: &actorID}}
		s := NewMediaTaskService(repo)
		s.reconciliationSettings = &reconciliationUnitSettings{timeout: 2 * time.Hour}
		actor := MediaTaskActor{UserID: 1, IsAdmin: admin}
		list, err := s.List(context.Background(), actor, MediaTaskFilter{})
		require.NoError(t, err)
		detail, err := s.Get(context.Background(), actor, 1)
		require.NoError(t, err)
		for _, item := range []*MediaTask{&list.Items[0], detail} {
			r := item.BillingReview
			require.Equal(t, completed.Add(2*time.Hour), *r.ExpiresAt)
			require.Equal(t, .3, r.HeldBalanceAmount)
			require.Equal(t, .2, r.HeldSubscriptionAmount)
			require.Equal(t, admin, r.CanCharge)
			require.Equal(t, admin, r.CanRelease)
			if admin {
				require.Equal(t, "内部核对记录", r.Reason)
				require.Equal(t, &actorID, r.ActorID)
			} else {
				require.Empty(t, r.Reason)
				require.Nil(t, r.ActorID)
			}
		}
	}
}

func TestMediaTaskReconciliationSummaryHasNoInventedDeadlineOnSettingFailure(t *testing.T) {
	repo := &reconciliationUnitRepo{task: MediaTask{ID: 1, Source: "async_image", Status: "failed", UpdatedAt: time.Now()}, review: &MediaTaskBillingReview{Status: "pending"}}
	s := NewMediaTaskService(repo)
	s.reconciliationSettings = &reconciliationUnitSettings{err: errors.New("settings unavailable")}
	actor := MediaTaskActor{UserID: 1, IsAdmin: true}
	list, err := s.List(context.Background(), actor, MediaTaskFilter{})
	require.NoError(t, err)
	detail, err := s.Get(context.Background(), actor, 1)
	require.NoError(t, err)
	require.Nil(t, list.Items[0].BillingReview.ExpiresAt)
	require.Nil(t, detail.BillingReview.ExpiresAt)
}

func TestMediaTaskReconciliationSummaryDeadlineOnlyForUnresolvedFailure(t *testing.T) {
	updated := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		status, billing string
		hasDeadline     bool
	}{
		{"failed", "pending", true}, {"cancelled", "pending", true}, {"expired", "pending", true},
		{"queued", "pending", false}, {"processing", "pending", false}, {"completed", "pending", false},
		{"failed", "released", false}, {"failed", "charged", false}, {"failed", "unreserved", false},
	} {
		t.Run(test.status+"_"+test.billing, func(t *testing.T) {
			repo := &reconciliationUnitRepo{task: MediaTask{ID: 1, Source: "async_image", Status: test.status, UpdatedAt: updated}, review: &MediaTaskBillingReview{Status: test.billing}}
			item, err := NewMediaTaskService(repo).Get(context.Background(), MediaTaskActor{UserID: 1, IsAdmin: true}, 1)
			require.NoError(t, err)
			if test.hasDeadline {
				require.NotNil(t, item.BillingReview.ExpiresAt)
				require.Equal(t, updated.Add(time.Hour), *item.BillingReview.ExpiresAt, "历史失败无完成时间时采用更新时间")
			} else {
				require.Nil(t, item.BillingReview.ExpiresAt)
			}
		})
	}
}

func TestMediaTaskReconciliationSummaryHandlesUnavailableAndExpiredLedger(t *testing.T) {
	actor := MediaTaskActor{UserID: 7, IsAdmin: true}
	repo := &reconciliationUnitRepo{task: MediaTask{ID: 1, Source: "async_image", Status: "failed"}, review: &MediaTaskBillingReview{Status: "pending", CanCharge: true, CanRelease: true}, reviewErr: errors.New("ledger unavailable")}
	s := NewMediaTaskService(repo)
	task, err := s.Get(context.Background(), actor, 1)
	require.NoError(t, err)
	require.Equal(t, "unavailable", task.BillingReview.Status)
	require.False(t, task.BillingReview.CanCharge)
	require.False(t, task.BillingReview.CanRelease)
	require.Nil(t, task.BillingReview.ExpiresAt)
	zero := 0.0
	repo.reviewErr = nil
	repo.review = &MediaTaskBillingReview{Status: "expired", ActualAmount: &zero}
	task, err = s.Get(context.Background(), actor, 1)
	require.NoError(t, err)
	require.Equal(t, "expired", task.Status)
	require.NotNil(t, task.ActualCost)
	require.Zero(t, *task.ActualCost)
}
