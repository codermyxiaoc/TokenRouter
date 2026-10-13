package repository

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/lib/pq"
)

// 显式管理员意图与读取时的账号快照分开传递，避免旧表单覆盖后台同步结果。
type upstreamBillingEditContextKey struct{}
type upstreamBillingEdit struct {
	probeEnabled    *bool
	rateSyncEnabled *bool
	rateMultiplier  *float64
}

// UpdateWithAccountBillingSettings 将账号编辑、开关联动与倍率保护放入同一行锁事务。
func (r *accountRepository) UpdateWithAccountBillingSettings(ctx context.Context, account *service.Account, probeEnabled, rateSyncEnabled *bool, rateMultiplier *float64) error {
	ctx = context.WithValue(ctx, upstreamBillingEditContextKey{}, upstreamBillingEdit{probeEnabled, rateSyncEnabled, rateMultiplier})
	return r.updateAccount(ctx, account)
}

// mergeLockedUpstreamBillingSettings 始终以锁定行中的系统状态为准，不信任表单回传快照。
func mergeLockedUpstreamBillingSettings(ctx context.Context, account *service.Account, extra map[string]any, identityUnchanged bool, probeJSON, syncJSON, snapshotJSON []byte, currentRate float64) error {
	for _, key := range []string{service.UpstreamBillingProbeEnabledExtraKey, service.UpstreamBillingRateSyncEnabledExtraKey, service.UpstreamBillingProbeExtraKey} {
		delete(extra, key)
	}
	edit, explicit := ctx.Value(upstreamBillingEditContextKey{}).(upstreamBillingEdit)
	if !service.IsUpstreamBillingProbeIdentity(account.Platform, account.Type) {
		return nil
	}
	var probeEnabled, syncEnabled bool
	_ = json.Unmarshal(probeJSON, &probeEnabled)
	_ = json.Unmarshal(syncJSON, &syncEnabled)
	wasSyncEnabled := probeEnabled && syncEnabled
	if edit.probeEnabled != nil {
		probeEnabled = *edit.probeEnabled
	}
	if edit.rateSyncEnabled != nil {
		syncEnabled = *edit.rateSyncEnabled
		if syncEnabled {
			if edit.probeEnabled != nil && !*edit.probeEnabled {
				return service.ErrUpstreamBillingProbeAccountInvalid
			}
			probeEnabled = true
		}
	}
	if !probeEnabled {
		syncEnabled = false
	}
	if len(probeJSON) > 0 || edit.probeEnabled != nil || (edit.rateSyncEnabled != nil && *edit.rateSyncEnabled) {
		extra[service.UpstreamBillingProbeEnabledExtraKey] = probeEnabled
	}
	if len(syncJSON) > 0 || edit.rateSyncEnabled != nil {
		extra[service.UpstreamBillingRateSyncEnabledExtraKey] = syncEnabled
	}
	if identityUnchanged && !(edit.probeEnabled != nil && !*edit.probeEnabled) {
		if snapshot, ok, err := decodeAccountExtraJSON(snapshotJSON); err != nil {
			return err
		} else if ok {
			extra[service.UpstreamBillingProbeExtraKey] = snapshot
		}
	}
	if explicit && edit.rateMultiplier != nil {
		if syncEnabled && *edit.rateMultiplier != currentRate {
			return service.ErrUpstreamBillingRateSyncConflict
		}
		account.RateMultiplier = edit.rateMultiplier
	} else if explicit || wasSyncEnabled || syncEnabled {
		// 未提交手动倍率时，保存其他字段不得将旧读取值写回覆盖已同步的倍率。
		account.RateMultiplier = &currentRate
	}
	return nil
}

// normalizeUpstreamBillingExtraPatch 保证关闭探测会停止同步，并删除失效观测。
func normalizeUpstreamBillingExtraPatch(updates map[string]any) {
	if enabled, ok := updates[service.UpstreamBillingProbeEnabledExtraKey].(bool); ok && !enabled {
		updates[service.UpstreamBillingRateSyncEnabledExtraKey] = false
		updates[service.UpstreamBillingProbeExtraKey] = nil
		return
	}
	if enabled, ok := updates[service.UpstreamBillingRateSyncEnabledExtraKey].(bool); ok && enabled {
		updates[service.UpstreamBillingProbeEnabledExtraKey] = true
	}
}

// 批量修改前锁定全部目标，避免检查后被并发开启同步，或只成功修改一部分账号。
func guardBulkUpstreamBillingSettings(ctx context.Context, exec sqlExecutor, ids []int64, updates service.AccountBulkUpdate) error {
	probe, hasProbe := updates.Extra[service.UpstreamBillingProbeEnabledExtraKey].(bool)
	syncRate, hasSync := updates.Extra[service.UpstreamBillingRateSyncEnabledExtraKey].(bool)
	if updates.RateMultiplier == nil && !hasProbe && !hasSync {
		return nil
	}
	rows, err := exec.QueryContext(ctx, `SELECT platform, type,
		COALESCE(extra @> '{"upstream_billing_probe_enabled": true}'::jsonb, false),
		COALESCE(extra @> '{"upstream_billing_rate_sync_enabled": true}'::jsonb, false)
		FROM accounts WHERE id = ANY($1) AND deleted_at IS NULL ORDER BY id FOR NO KEY UPDATE`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var platform, accountType string
		var currentProbe, currentSync bool
		if err := rows.Scan(&platform, &accountType, &currentProbe, &currentSync); err != nil {
			return err
		}
		if (hasProbe && probe || hasSync && syncRate) && !service.IsUpstreamBillingProbeIdentity(platform, accountType) {
			return service.ErrUpstreamBillingProbeAccountInvalid
		}
		if hasProbe {
			currentProbe = probe
		}
		if hasSync {
			currentSync = syncRate
		}
		if updates.RateMultiplier != nil && currentProbe && currentSync {
			return service.ErrUpstreamBillingRateSyncBulkConflict
		}
	}
	return rows.Err()
}

func upstreamBillingSnapshotClearRequested(updates map[string]any) bool {
	value, exists := updates[service.UpstreamBillingProbeExtraKey]
	return exists && value == nil
}

// UpdateUpstreamBillingProbeSnapshot 使用身份、开关、快照和当前倍率 CAS 防止过期探测覆盖管理员更新。
func (r *accountRepository) UpdateUpstreamBillingProbeSnapshot(
	ctx context.Context,
	account *service.Account,
	snapshot *service.UpstreamBillingProbeSnapshot,
	rateMultiplier *float64,
) error {
	if account == nil || snapshot == nil {
		return service.ErrAccountNilInput
	}
	if rateMultiplier != nil && (math.IsNaN(*rateMultiplier) || math.IsInf(*rateMultiplier, 0) || *rateMultiplier <= 0 || *rateMultiplier > 100) {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	if snapshot.Status != service.UpstreamBillingProbeStatusOK {
		rateMultiplier = nil
	}
	if dbent.TxFromContext(ctx) == nil {
		tx, err := r.client.Tx(ctx)
		if errors.Is(err, dbent.ErrTxStarted) {
			return r.updateUpstreamBillingProbeSnapshotInTx(ctx, account, snapshot, rateMultiplier)
		}
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()

		if err := r.updateUpstreamBillingProbeSnapshotInTx(dbent.NewTxContext(ctx, tx), account, snapshot, rateMultiplier); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		r.syncSchedulerAccountSnapshot(ctx, account.ID)
		return nil
	}
	return r.updateUpstreamBillingProbeSnapshotInTx(ctx, account, snapshot, rateMultiplier)
}

func (r *accountRepository) updateUpstreamBillingProbeSnapshotInTx(
	ctx context.Context,
	account *service.Account,
	snapshot *service.UpstreamBillingProbeSnapshot,
	rateMultiplier *float64,
) error {
	payload, err := json.Marshal(map[string]any{service.UpstreamBillingProbeExtraKey: snapshot})
	if err != nil {
		return err
	}
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return err
	}
	accountExtra, err := json.Marshal(normalizeJSONMap(account.Extra))
	if err != nil {
		return err
	}
	var expectedSnapshot any
	if account.Extra != nil {
		expectedSnapshot = account.Extra[service.UpstreamBillingProbeExtraKey]
	}
	expectedSnapshotJSON, err := json.Marshal(expectedSnapshot)
	if err != nil {
		return err
	}
	var expectedEnabled any
	if account.Extra != nil {
		expectedEnabled = account.Extra[service.UpstreamBillingProbeEnabledExtraKey]
	}
	expectedEnabledJSON, err := json.Marshal(expectedEnabled)
	if err != nil {
		return err
	}
	var expectedRateSyncEnabled any
	if account.Extra != nil {
		expectedRateSyncEnabled = account.Extra[service.UpstreamBillingRateSyncEnabledExtraKey]
	}
	expectedRateSyncEnabledJSON, err := json.Marshal(expectedRateSyncEnabled)
	if err != nil {
		return err
	}
	client := clientFromContext(ctx, r.client)
	proxyMatches, err := lockAndMatchAccountProxyIdentity(ctx, client, account)
	if err != nil {
		return err
	}
	if !proxyMatches {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	var proxyID any
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	result, err := client.ExecContext(ctx, `
		UPDATE accounts
		SET
			extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb,
			rate_multiplier = CASE
				WHEN $10::numeric IS NOT NULL
					AND extra @> '{"upstream_billing_probe_enabled": true}'::jsonb
					AND extra @> '{"upstream_billing_rate_sync_enabled": true}'::jsonb
				THEN $10::numeric
				ELSE rate_multiplier
			END,
			updated_at = NOW()
		WHERE id = $2
			AND platform = $3
			AND type = $4
			AND credentials = $5::jsonb
			AND proxy_id IS NOT DISTINCT FROM $6
			AND COALESCE(extra -> 'upstream_billing_probe', 'null'::jsonb) = $7::jsonb
			AND COALESCE(extra -> 'upstream_billing_probe_enabled', 'null'::jsonb) = $8::jsonb
			AND COALESCE(extra -> 'upstream_billing_rate_sync_enabled', 'null'::jsonb) = $9::jsonb
			AND rate_multiplier IS NOT DISTINCT FROM $11::numeric
			AND extra -> 'enable_tls_fingerprint' IS NOT DISTINCT FROM $12::jsonb -> 'enable_tls_fingerprint'
			AND extra -> 'tls_fingerprint_profile_id' IS NOT DISTINCT FROM $12::jsonb -> 'tls_fingerprint_profile_id'
			AND extra -> 'tls_fingerprint_router_id' IS NOT DISTINCT FROM $12::jsonb -> 'tls_fingerprint_router_id'
			AND deleted_at IS NULL
	`, string(payload), account.ID, account.Platform, account.Type, string(credentials), proxyID, string(expectedSnapshotJSON), string(expectedEnabledJSON), string(expectedRateSyncEnabledJSON), rateMultiplier, account.RateMultiplier, string(accountExtra))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrUpstreamBillingProbeIdentityChanged
	}
	return enqueueSchedulerOutbox(ctx, client, service.SchedulerOutboxEventAccountChanged, &account.ID, nil, nil)
}

// ListDueUpstreamBillingProbeAccounts 只装载最早到期的有界候选，并兼容纳秒时间戳及坏数据。
func (r *accountRepository) ListDueUpstreamBillingProbeAccounts(ctx context.Context, now time.Time, limit int) ([]service.Account, error) {
	if limit <= 0 {
		return []service.Account{}, nil
	}
	if r.sql == nil {
		return nil, errors.New("account repository SQL executor not configured")
	}

	rows, err := r.sql.QueryContext(ctx, `
		WITH candidates AS (
			SELECT
				id,
				extra #>> '{upstream_billing_probe,status}' AS probe_status,
				extra #>> '{upstream_billing_probe,next_probe_at}' AS next_probe_at
			FROM accounts
			WHERE deleted_at IS NULL
				AND status = 'active'
				AND type = 'apikey'
				AND platform = ANY($3)
				AND extra @> '{"upstream_billing_probe_enabled": true}'::jsonb
		), parsed AS MATERIALIZED (
			SELECT
				id,
				probe_status,
				next_probe_at,
				next_probe_at ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$' AS rfc3339_shape,
				jsonb_path_query_first_tz(
					jsonb_build_object(
						'value',
						replace(regexp_replace(regexp_replace(
							next_probe_at,
							'(\.[0-9]{6})[0-9]+(Z|[+-][0-9]{2}:[0-9]{2})$',
							'\1\2'
						), 'Z$', '+00:00'), 'T', ' ')
					),
					'$.value.datetime()',
					'{}'::jsonb,
					true
				) #>> '{}' AS parsed_next_probe_at
			FROM candidates
		), normalized AS (
			SELECT
				id,
				probe_status,
				next_probe_at,
				parsed_next_probe_at,
				rfc3339_shape AND parsed_next_probe_at IS NOT NULL AS valid_next_probe_at
			FROM parsed
		)
		SELECT id
		FROM normalized
		WHERE probe_status NOT IN ('ok', 'unsupported', 'failed')
			OR probe_status IS NULL
			OR next_probe_at IS NULL
			OR NOT valid_next_probe_at
			OR CASE WHEN valid_next_probe_at THEN parsed_next_probe_at::timestamptz <= $1 ELSE FALSE END
		ORDER BY
			CASE
				WHEN probe_status NOT IN ('ok', 'unsupported', 'failed')
					OR probe_status IS NULL
					OR next_probe_at IS NULL
					OR NOT valid_next_probe_at
				THEN 0
				ELSE 1
			END ASC,
			CASE WHEN valid_next_probe_at THEN parsed_next_probe_at::timestamptz END ASC NULLS FIRST,
			id ASC
		LIMIT $2
	`, now.UTC(), limit, pq.Array(service.UpstreamBillingProbePlatforms()))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []service.Account{}, nil
	}

	if err := rows.Close(); err != nil {
		return nil, err
	}
	accounts, err := r.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]service.Account, 0, len(accounts))
	for _, account := range accounts {
		if account != nil {
			out = append(out, *account)
		}
	}
	return out, nil
}
