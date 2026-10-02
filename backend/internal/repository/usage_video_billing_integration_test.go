//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

// 使用真实 PostgreSQL 执行查询和归属匹配，CTE 隔离数据不修改业务表。
type usageVideoBillingFixtureQueryer struct {
	db      *sql.DB
	empty   bool
	queries int
}

func TestUsageVideoBillingRepositoryListAndGetUseHistoricalSnapshot(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newUsageLogRepositoryWithSQL(client, tx)
	user := mustCreateUser(t, client, &service.User{Email: "video-usage-display@example.com"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-video-usage-display"})
	account := mustCreateAccount(t, client, &service.Account{Name: "video-usage-display"})
	group := mustCreateGroup(t, client, &service.Group{Name: "video-usage-display", Platform: service.PlatformVideo})
	mode, resolution, duration := "video", "768p", 5
	task := &service.VideoTaskRecord{Quote: &service.VideoPriceQuote{Mode: service.BillingModeVideo,
		Unit: "second", UnitPrice: .425, Resolution: resolution}, Metadata: service.VideoRequestMetadata{DurationSeconds: 5}}
	raw, err := json.Marshal(task)
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, `INSERT INTO video_tasks
		(id,user_id,api_key_id,account_id,group_id,protocol,payload_hash,status,billing_status,record)
		VALUES('vid_usage_display',$1,$2,$3,$4,'compat','hash','completed','settled',$5)`, user.ID, key.ID, account.ID, group.ID, string(raw))
	require.NoError(t, err)
	log := &service.UsageLog{UserID: user.ID, APIKeyID: key.ID, AccountID: account.ID, GroupID: &group.ID,
		RequestID: "video_capture:vid_usage_display", Model: "MiniMax-H3-Max", BillingMode: &mode,
		VideoCount: 1, VideoResolution: &resolution, VideoDurationSeconds: &duration,
		OutputCost: 2.125, TotalCost: 2.125, ActualCost: 2.125, CreatedAt: time.Now()}
	_, err = repo.Create(ctx, log)
	require.NoError(t, err)
	loaded, err := repo.GetByID(ctx, log.ID)
	require.NoError(t, err)
	require.NotNil(t, loaded.VideoBilling)
	require.Equal(t, .425, loaded.VideoBilling.UnitPrice)
	require.Equal(t, 5.0, loaded.VideoBilling.DurationSeconds)
	require.Equal(t, loaded.OutputCost, loaded.VideoBilling.UnitPrice*loaded.VideoBilling.DurationSeconds)
	logs, _, err := repo.ListByUser(ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, loaded.VideoBilling, logs[0].VideoBilling, "用户、管理员及导出共享的列表查询与详情使用同一快照")
}

func (q *usageVideoBillingFixtureQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries++
	const fixture = `WITH video_tasks(id, user_id, api_key_id, billing_status, record) AS (VALUES
	('vid_ok'::text, 7::bigint, 10::bigint, 'settled'::text,
	 '{"Quote":{"mode":"video","unit":"second","unit_price":0.425,"resolution":"768p","reference_image_count":7,"image_input_pricing":{"free_images":5,"price":0.15}},"Metadata":{"duration_seconds":2.5,"reference_image_count":99},"RawResponse":"private","VideoURL":"https://private.example/video","Target":{"base_url":"https://private-upstream"}}'::jsonb),
	('vid_no_fee', 7, 10, 'settled', '{"Quote":{"mode":"video_token","unit":"million_tokens","unit_price":8.74,"resolution":"480p"},"Metadata":{"tokens":40594,"reference_image_count":3,"duration_seconds":4}}'),
	('vid_old', 7, 10, 'settled', '{}'),
	('vid_bad', 7, 10, 'settled', '{"Quote":"bad","Metadata":{}}'),
	('vid_missing_price', 7, 10, 'settled', '{"Quote":{"mode":"video","unit":"second"},"Metadata":{}}'),
	('vid_null_price', 7, 10, 'settled', '{"Quote":{"mode":"video","unit":"second","unit_price":null},"Metadata":{}}'),
	('vid_pending', 7, 10, 'pending', '{"Quote":{"mode":"video","unit":"second","unit_price":0.3},"Metadata":{}}')
)
`
	if q.empty {
		return q.db.QueryContext(ctx, `WITH video_tasks(id, user_id, api_key_id, billing_status, record) AS
		(SELECT ''::text, 0::bigint, 0::bigint, ''::text, '{}'::jsonb WHERE FALSE) `+query, args...)
	}
	return q.db.QueryContext(ctx, fixture+query, args...)
}

func TestUsageVideoBillingSnapshotIsolationAndMissingHistory(t *testing.T) {
	queryer := &usageVideoBillingFixtureQueryer{db: integrationDB}
	makeLog := func(id string, userID, keyID int64) *service.UsageLog {
		return &service.UsageLog{UserID: userID, BillingUserID: 99, APIKeyID: keyID, VideoCount: 1, RequestID: "video_capture:" + id, ImageInputCost: .3}
	}
	logs := []*service.UsageLog{
		makeLog("vid_ok", 7, 10), makeLog("vid_no_fee", 7, 10),
		makeLog("vid_ok", 99, 10), makeLog("vid_ok", 7, 11),
		makeLog("vid_old", 7, 10), makeLog("vid_bad", 7, 10), makeLog("vid_pending", 7, 10), makeLog("vid_removed", 7, 10),
		makeLog("vid_missing_price", 7, 10), makeLog("vid_null_price", 7, 10),
	}
	require.NoError(t, hydrateUsageVideoBilling(context.Background(), queryer, logs))
	require.Equal(t, 1, queryer.queries, "包含多任务及旧数据的一页只执行一次快照查询")
	require.Equal(t, 2.5, logs[0].VideoBilling.DurationSeconds)
	require.Equal(t, 7, *logs[0].VideoBilling.ReferenceImageCount)
	require.Equal(t, 2, *logs[0].VideoBilling.BillableReferenceImageCount)
	require.Equal(t, .3, *logs[0].VideoBilling.ReferenceImageCost)
	require.Equal(t, 3, *logs[1].VideoBilling.ReferenceImageCount)
	require.Nil(t, logs[1].VideoBilling.ReferenceImageCost)
	require.Equal(t, int64(40594), *logs[1].VideoBilling.Tokens)
	for _, log := range logs[2:] {
		require.Nil(t, log.VideoBilling, "用户或 Key 不匹配、非结算、缺少或损坏快照均不能泄漏或猜测")
	}
	queryer.empty = true
	require.NoError(t, hydrateUsageVideoBilling(context.Background(), queryer, logs))
	for _, log := range logs {
		require.Nil(t, log.VideoBilling, "历史任务表无数据仍可读取原使用记录")
	}
}
