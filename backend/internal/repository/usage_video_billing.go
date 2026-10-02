package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

type usageVideoBillingSource struct {
	taskID string
	userID int64
	keyID  int64
}

type usageVideoBillingSnapshot struct {
	quote    *service.VideoPriceQuote
	metadata service.VideoRequestMetadata
}

// hydrateUsageVideoBilling 为当前页批量读取已结算视频的计价快照，普通用量不增加查询。
// @project-doc docs/domains/routing_and_billing.md#usage_video_pricing
func hydrateUsageVideoBilling(ctx context.Context, db sqlQueryer, logs []*service.UsageLog) error {
	var sources []usageVideoBillingSource
	seen := make(map[usageVideoBillingSource]struct{})
	for _, log := range logs {
		if log == nil {
			continue
		}
		log.VideoBilling = nil
		source, ok := usageVideoBillingSourceFor(log)
		if !ok {
			continue
		}
		if _, exists := seen[source]; !exists {
			seen[source] = struct{}{}
			sources = append(sources, source)
		}
	}
	if len(sources) == 0 {
		return nil
	}
	snapshots := make(map[usageVideoBillingSource]usageVideoBillingSnapshot, len(sources))
	// 导出与大页面同样保持有界批量，不按每条任务执行额外查询。
	const batchSize = 500
	for start := 0; start < len(sources); start += batchSize {
		if err := loadUsageVideoBillingSnapshots(ctx, db, sources[start:min(start+batchSize, len(sources))], snapshots); err != nil {
			return err
		}
	}
	for _, log := range logs {
		source, ok := usageVideoBillingSourceFor(log)
		if !ok {
			continue
		}
		if snapshot, exists := snapshots[source]; exists {
			log.VideoBilling = service.BuildUsageVideoBillingDetails(log, snapshot.quote, snapshot.metadata)
		}
	}
	return nil
}

func usageVideoBillingSourceFor(log *service.UsageLog) (usageVideoBillingSource, bool) {
	if log == nil || log.VideoCount <= 0 || log.UserID <= 0 || log.APIKeyID <= 0 {
		return usageVideoBillingSource{}, false
	}
	id, ok := strings.CutPrefix(log.RequestID, "video_capture:")
	if !ok || id == "" {
		return usageVideoBillingSource{}, false
	}
	return usageVideoBillingSource{taskID: id, userID: log.UserID, keyID: log.APIKeyID}, true
}

func loadUsageVideoBillingSnapshots(ctx context.Context, db sqlQueryer, sources []usageVideoBillingSource, snapshots map[usageVideoBillingSource]usageVideoBillingSnapshot) (err error) {
	values, args := make([]string, 0, len(sources)), make([]any, 0, len(sources)*3)
	for _, source := range sources {
		position := len(args) + 1
		values = append(values, fmt.Sprintf("($%d::text, $%d::bigint, $%d::bigint)", position, position+1, position+2))
		args = append(args, source.taskID, source.userID, source.keyID)
	}
	// 只选允许展示的两个子对象；绑定行为用户和原 Key，团队付款人不替代任务所有者。
	query := `SELECT source.task_id, source.user_id, source.api_key_id,
		task.record->'Quote', task.record->'Metadata'
		FROM (VALUES ` + strings.Join(values, ", ") + `) AS source(task_id, user_id, api_key_id)
		JOIN video_tasks task ON task.id = source.task_id
			AND task.user_id = source.user_id AND task.api_key_id = source.api_key_id
		WHERE task.billing_status = 'settled'`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("load usage video billing snapshots: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var source usageVideoBillingSource
		var quoteJSON, metadataJSON []byte
		if err = rows.Scan(&source.taskID, &source.userID, &source.keyID, &quoteJSON, &metadataJSON); err != nil {
			return err
		}
		var snapshot usageVideoBillingSnapshot
		var requiredPrice struct {
			UnitPrice *float64 `json:"unit_price"`
		}
		// 老数据缺少或损坏快照时保留原使用记录，不能猜价格或令整页不可用。
		if json.Unmarshal(quoteJSON, &requiredPrice) != nil || requiredPrice.UnitPrice == nil ||
			json.Unmarshal(quoteJSON, &snapshot.quote) != nil || snapshot.quote == nil ||
			len(metadataJSON) == 0 || string(metadataJSON) == "null" || json.Unmarshal(metadataJSON, &snapshot.metadata) != nil {
			continue
		}
		snapshots[source] = snapshot
	}
	return rows.Err()
}
