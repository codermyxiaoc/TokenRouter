//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// 音频夹具仅在 integration_harness 创建的隔离 PostgreSQL/Redis 内生效，不使用真实上游。
type creativeAudioIntegrationSettings struct{ groupID int64 }

func (creativeAudioIntegrationSettings) IsCreativeEnabled(context.Context) bool { return true }

// GetCreativeCanvasAssetRetention 集成测试不需要画布素材，沿用默认七天。
func (creativeAudioIntegrationSettings) GetCreativeCanvasAssetRetention(context.Context) time.Duration {
	return service.CreativeCanvasAssetTTL
}
func (s creativeAudioIntegrationSettings) GetCreativeModelSettings(context.Context) []service.CreativeModelSetting {
	return []service.CreativeModelSetting{{GroupID: s.groupID, Model: service.CreativeGrokTTSModel,
		MediaType: "audio", Operations: []string{service.CreativeOperationGenerate}}}
}

type creativeAudioIntegrationFixture struct {
	canvas                     *service.CreativeCanvasService
	canvasRepo                 *creativeCanvasRepository
	svc                        *service.CreativePublicService
	userID, groupID, accountID int64
	scope                      service.CreativeRunScope
}

func newCreativeAudioIntegrationFixture(t *testing.T, price *float64) *creativeAudioIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{Email: "audio-billing-" + uuid.NewString() + "@example.com", Balance: 10})
	group := mustCreateGroup(t, client, &service.Group{Name: "audio-" + uuid.NewString(), Platform: service.PlatformGrok, RateMultiplier: 1.5})
	// 单独的图片倍率故意设为 9，音频仍应使用分组基础倍率 1.5。
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET allow_image_generation=FALSE,image_rate_independent=TRUE,image_rate_multiplier=9,audio_tts_price_per_million_chars=$2 WHERE id=$1`, group.ID, price)
	require.NoError(t, err)
	account := mustCreateAccount(t, client, &service.Account{Name: "voice-" + uuid.NewString(), Platform: service.PlatformGrok,
		Type: service.AccountTypeAPIKey, Credentials: map[string]any{"model_whitelist": []string{service.CreativeGrokTTSSelectionModel}}})
	_, err = client.AccountGroup.Create().SetAccountID(account.ID).SetGroupID(group.ID).Save(ctx)
	require.NoError(t, err)
	rdb := testRedis(t)
	cfg := &config.Config{Creative: config.CreativeConfig{Enabled: true, TransientTTLSeconds: 600}}
	keyRepo, ok := NewAPIKeyRepository(client, integrationDB).(service.CreativeManagedKeyRepository)
	require.True(t, ok)
	svc := &service.CreativePublicService{
		Repo: NewCreativeRunRepository(client), ApiKeyRepo: keyRepo, UserRepo: NewUserRepository(client, integrationDB),
		AccountRepo: NewAccountRepository(client, integrationDB, nil), GroupRepo: NewGroupRepository(client, integrationDB),
		Queue: NewCreativeQueue(rdb, cfg), TransientStore: NewCreativeTransientStore(rdb, cfg),
		BillingRepo: NewUsageBillingRepository(client, integrationDB), UsageLogRepo: NewUsageLogRepository(client, integrationDB, nil),
		Outbox: NewCreativeRunOutboxRepository(integrationDB), Settings: creativeAudioIntegrationSettings{groupID: group.ID}, Config: cfg,
	}
	insertSettlementPlatformQuota(t, user.ID, service.PlatformGrok, float64Ptr(100))
	store, err := service.NewCreativeCanvasLocalStore(t.TempDir())
	require.NoError(t, err)
	canvasRepo := &creativeCanvasRepository{db: integrationDB}
	canvasService := service.NewCreativeCanvasService(canvasRepo, store, svc)
	canvas, err := canvasService.Create(ctx, user.ID, "音频账务集成", json.RawMessage(`{"nodes":[{"id":"audio","type":"audio"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`))
	require.NoError(t, err)
	svc.CloudOutputSink = canvasService
	return &creativeAudioIntegrationFixture{svc: svc, canvas: canvasService, canvasRepo: canvasRepo, userID: user.ID, groupID: group.ID, accountID: account.ID,
		scope: service.CreativeRunScope{UserID: user.ID, WorkspaceID: canvas.ID}}
}

func (f *creativeAudioIntegrationFixture) params() service.CreativeCanvasExecutionParams {
	// 用多字节汉字验证按字符计费，而非按 UTF-8 字节数多收费用。
	return service.CreativeCanvasExecutionParams{GroupID: f.groupID, Model: service.CreativeGrokTTSModel,
		Operation: service.CreativeOperationGenerate, Prompt: strings.Repeat("语", 2000), Voice: "eve", AudioFormat: "mp3", Speed: 1}
}

// 经画布入口创建，保留服务私有 canvasAudio 门禁；Reconcile 只登记任务，不启动真实 provider worker。
func (f *creativeAudioIntegrationFixture) createRun(t *testing.T, key string) *service.CreativeRunPublic {
	t.Helper()
	ctx := context.Background()
	execution, err := f.canvas.CreateExecution(ctx, f.userID, f.scope.WorkspaceID, "audio", "audio", f.params(), key)
	require.NoError(t, err)
	f.canvas.Reconcile(ctx)
	execution, err = f.canvasRepo.GetExecution(ctx, f.userID, execution.ID)
	require.NoError(t, err)
	require.NotEmpty(t, execution.TaskID, "音频任务应从画布登记，实际错误：%s %s", execution.ErrorCode, execution.ErrorMessage)
	run, err := f.svc.GetRun(ctx, f.scope, execution.TaskID)
	require.NoError(t, err)
	return run
}

func (f *creativeAudioIntegrationFixture) assertMoney(t *testing.T, spent, frozen float64) {
	t.Helper()
	var balance, actualFrozen float64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT balance,frozen_balance FROM users WHERE id=$1`, f.userID).Scan(&balance, &actualFrozen))
	require.InDelta(t, 10-spent-frozen, balance, 1e-8)
	require.InDelta(t, frozen, actualFrozen, 1e-8)
	assertSettlementPlatformQuota(t, f.userID, service.PlatformGrok, spent)
}

// 固定音频模拟已经收到的上游成果，避免任何测试创建收费请求。
func (f *creativeAudioIntegrationFixture) recordSuccess(t *testing.T, runID string) []byte {
	t.Helper()
	ctx := context.Background()
	content := []byte("ID3\x04\x00\x00\x00\x00\x00\x00local-audio-result")
	require.NoError(t, f.svc.MarkRunning(ctx, runID, f.accountID))
	_, err := f.svc.SucceedRun(ctx, runID, f.accountID, []service.CreativeOutputResult{{Index: 0, Success: true, Bytes: content, Mime: "audio/mpeg"}})
	require.NoError(t, err)
	return content
}

func TestCreativeAudioBillingCaptureAndSettlementReplay(t *testing.T) {
	f := newCreativeAudioIntegrationFixture(t, float64Ptr(20))
	ctx := context.Background()
	run := f.createRun(t, "audio-success")
	require.Equal(t, service.CreativeOperationTTS, run.Operation)
	require.Equal(t, 1, run.RequestedOutputCount)
	require.InDelta(t, 0.06, run.HoldAmount, 1e-8)
	f.assertMoney(t, 0, 0.06)
	content := f.recordSuccess(t, run.ID)
	// 改变隔离夹具价卡不能覆盖任务创建时冻结的整段文本金额。
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET audio_tts_price_per_million_chars=999,rate_multiplier=9 WHERE id=$1`, f.groupID)
	require.NoError(t, err)
	creativeMJParallel(t, 24, func() error { return f.svc.SettleRun(ctx, run.ID) })
	f.assertMoney(t, 0.06, 0)
	fresh, err := f.svc.GetRun(ctx, f.scope, run.ID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusSucceeded, fresh.Status)
	require.Len(t, fresh.Outputs, 1)
	output, err := f.svc.GetOutputContent(ctx, f.scope, run.ID, 0)
	require.NoError(t, err)
	require.Equal(t, content, output.Content)
	assets, total, err := f.canvas.ListAssets(ctx, f.userID, f.scope.WorkspaceID, 20, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, assets, 1)
	require.Equal(t, "audio", assets[0].MediaType)
	require.Equal(t, "audio/mpeg", assets[0].MIMEType)
	require.WithinDuration(t, time.Now().Add(7*24*time.Hour), assets[0].ExpiresAt, time.Minute)
	_, stream, err := f.canvas.OpenAsset(ctx, f.userID, assets[0].ID)
	require.NoError(t, err)
	archived, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	require.Equal(t, content, archived)
	other := mustCreateUser(t, integrationEntClient, &service.User{})
	_, foreignStream, err := f.canvas.OpenAsset(ctx, other.ID, assets[0].ID)
	require.ErrorIs(t, err, service.ErrCanvasNotFound)
	require.Nil(t, foreignStream)
	var holds, captures, logs, imageCount int
	var amount float64
	var endpoint, billingMode string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE request_id=$1),count(*) FILTER(WHERE request_id=$2) FROM usage_billing_dedup`, service.CreativeHoldRequestID(run.ID), service.CreativeCaptureRequestID(run.ID)).Scan(&holds, &captures))
	require.Equal(t, 1, holds)
	require.Equal(t, 1, captures)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(max(image_count),0),COALESCE(sum(actual_cost),0),COALESCE(max(upstream_endpoint),''),COALESCE(max(billing_mode),'') FROM usage_logs WHERE request_id=$1`, service.CreativeSettlementRequestID(run.ID)).Scan(&logs, &imageCount, &amount, &endpoint, &billingMode))
	require.Equal(t, 1, logs)
	require.Zero(t, imageCount)
	require.InDelta(t, 0.06, amount, 1e-8)
	require.Equal(t, "/v1/tts", endpoint)
	require.Equal(t, "per_request", billingMode)
	// 重启后的终态重放和迟到释放不能二次扣费或错误退款。
	recovered := &service.CreativePublicService{Repo: NewCreativeRunRepository(integrationEntClient), BillingRepo: NewUsageBillingRepository(integrationEntClient, integrationDB)}
	for i := 0; i < 3; i++ {
		require.NoError(t, recovered.SettleRun(ctx, run.ID))
		require.NoError(t, recovered.ReleaseRun(ctx, run.ID))
	}
	f.assertMoney(t, 0.06, 0)
}

func TestCreativeAudioBillingKnownFailureReleaseReplay(t *testing.T) {
	f := newCreativeAudioIntegrationFixture(t, float64Ptr(20))
	ctx := context.Background()
	creativeMJParallel(t, 24, func() error {
		_, err := f.canvas.CreateExecution(ctx, f.userID, f.scope.WorkspaceID, "audio", "audio", f.params(), "audio-same-request")
		return err
	})
	publicRun := f.createRun(t, "audio-same-request")
	run, err := f.svc.Repo.GetCreativeRunByRunID(ctx, publicRun.ID)
	require.NoError(t, err)
	var runs, holds, outputs int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM creative_runs WHERE user_id=$1`, f.userID).Scan(&runs))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM usage_billing_dedup WHERE request_id=$1`, service.CreativeHoldRequestID(run.RunID)).Scan(&holds))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM creative_run_outputs WHERE run_id=$1`, run.RunID).Scan(&outputs))
	require.Equal(t, 1, runs)
	require.Equal(t, 1, holds)
	require.Equal(t, 1, outputs)
	f.assertMoney(t, 0, 0.06)
	require.NoError(t, f.svc.MarkRunning(ctx, run.RunID, f.accountID))
	require.NoError(t, f.svc.FailRun(ctx, run.RunID, "UPSTREAM_REJECTED", "isolated explicit rejection"))
	creativeMJParallel(t, 24, func() error { return f.svc.ReleaseRun(ctx, run.RunID) })
	f.assertMoney(t, 0, 0)
	var releases, captures int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE request_id=$1),count(*) FILTER(WHERE request_id=$2) FROM usage_billing_dedup`, service.CreativeReleaseRequestID(run.RunID), service.CreativeCaptureRequestID(run.RunID)).Scan(&releases, &captures))
	require.Equal(t, 1, releases)
	require.Zero(t, captures)
	fresh, err := f.svc.GetRun(ctx, f.scope, run.RunID)
	require.NoError(t, err)
	require.Equal(t, service.CreativeRunStatusFailed, fresh.Status)
	next := f.createRun(t, "audio-next-request")
	require.NoError(t, f.svc.ReleaseRun(ctx, run.RunID))
	f.assertMoney(t, 0, 0.06)
	require.NoError(t, f.svc.FailRun(ctx, next.ID, "UPSTREAM_REJECTED", "isolated cleanup"))
	f.assertMoney(t, 0, 0)
}

func TestCreativeAudioBillingDefaultAndExplicitFree(t *testing.T) {
	for _, tc := range []struct {
		name  string
		price *float64
		want  float64
	}{{name: "NULL使用默认15美元每百万字符", want: 0.045}, {name: "显式零价不回退", price: float64Ptr(0), want: 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCreativeAudioIntegrationFixture(t, tc.price)
			ctx := context.Background()
			run := f.createRun(t, "audio-price-boundary")
			require.InDelta(t, tc.want, run.HoldAmount, 1e-8)
			f.assertMoney(t, 0, tc.want)
			f.recordSuccess(t, run.ID)
			creativeMJParallel(t, 8, func() error { return f.svc.SettleRun(ctx, run.ID) })
			f.assertMoney(t, tc.want, 0)
			fresh, err := f.svc.GetRun(ctx, f.scope, run.ID)
			require.NoError(t, err)
			require.Equal(t, service.CreativeRunStatusSucceeded, fresh.Status)
			var logs int
			var actual float64
			require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*),COALESCE(sum(actual_cost),0) FROM usage_logs WHERE request_id=$1`, service.CreativeSettlementRequestID(run.ID)).Scan(&logs, &actual))
			require.Equal(t, 1, logs)
			require.InDelta(t, tc.want, actual, 1e-8)
		})
	}
}

func TestCreativeAudioCanvasMigrationAndOwnerIsolation(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	var migrations int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE filename='311_creative_canvas_audio.sql'`).Scan(&migrations))
	require.Equal(t, 1, migrations, "测试 schema 必须通过正式迁移 runner 执行 311")
	u := mustCreateUser(t, client, &service.User{})
	other := mustCreateUser(t, client, &service.User{})
	r := &creativeCanvasRepository{db: integrationDB}
	canvas := &service.CreativeCanvas{ID: uuid.NewString(), UserID: u.ID, Title: "隔离音频回归", Document: json.RawMessage(`{"nodes":[{"id":"audio-node","type":"audio"}],"connections":[],"viewport":{"x":0,"y":0,"k":1}}`)}
	require.NoError(t, r.Create(ctx, canvas))
	execution := &service.CreativeCanvasExecution{ID: uuid.NewString(), UserID: u.ID, CanvasID: canvas.ID, NodeID: "audio-node", Kind: "audio", IdempotencyKey: uuid.NewString(), Fingerprint: "audio-settings-v1", Params: service.CreativeCanvasExecutionParams{Model: service.CreativeGrokTTSModel, Prompt: "本地测试音频", Voice: "eve", AudioFormat: "mp3", Speed: 1}}
	created, err := r.CreateExecution(ctx, execution)
	require.NoError(t, err)
	require.Equal(t, "audio", created.Kind)
	require.Equal(t, "eve", created.Params.Voice)
	replayed, err := r.CreateExecution(ctx, execution)
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID)
	_, err = r.GetExecution(ctx, other.ID, created.ID)
	require.ErrorIs(t, err, service.ErrCanvasNotFound)
	foreign, total, err := r.ListExecutions(ctx, other.ID, canvas.ID, 20, 0)
	require.NoError(t, err)
	require.Empty(t, foreign)
	require.Zero(t, total)
	unauthorized := *execution
	unauthorized.ID, unauthorized.IdempotencyKey = uuid.NewString(), uuid.NewString()
	unauthorized.UserID = other.ID
	_, err = r.CreateExecution(ctx, &unauthorized)
	require.ErrorIs(t, err, service.ErrCanvasNotFound)
	changed := *execution
	changed.Fingerprint = "different-voice"
	_, err = r.CreateExecution(ctx, &changed)
	require.ErrorIs(t, err, service.ErrCanvasConflict)
	// 迁移只新增 audio，不能放开任意执行类型。
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO creative_canvas_executions(id,user_id,canvas_id,node_id,kind,params,idempotency_key,fingerprint) VALUES($1,$2,$3,'audio-node','unsupported','{}'::jsonb,$4,'invalid')`, uuid.NewString(), u.ID, canvas.ID, uuid.NewString())
	require.Error(t, err)
}
