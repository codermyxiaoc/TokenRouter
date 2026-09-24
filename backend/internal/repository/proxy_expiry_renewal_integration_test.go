//go:build integration

package repository

import (
	"github.com/TokenFlux/TokenRouter/internal/service"
	"time"
)

// 固定复现快照读取、管理员更新、到期扫描的顺序，避免依赖休眠。
func (s *ProxyExpirySuite) TestSweep_SkipsChangedSnapshot() {
	for _, edit := range []string{"renew", "clear expiry", "disable", "change mode", "change backup"} {
		s.Run(edit, func() {
			now := time.Now()
			past := now.Add(-time.Hour)
			future := now.Add(24 * time.Hour)
			backup := s.mkProxy("snapshot-backup", service.FallbackModeNone, &future, nil)
			source := s.mkProxy("snapshot-source", service.FallbackModeDirect, &past, nil)
			if edit == "change backup" {
				_, err := s.tx.ExecContext(s.ctx, `UPDATE proxies SET fallback_mode='proxy', backup_proxy_id=$1 WHERE id=$2`, backup, source)
				s.Require().NoError(err)
			}
			account := s.mkAccountWithProxy(source)
			snapshot, err := s.repo.GetByID(s.ctx, source)
			s.Require().NoError(err)
			target, change := service.ResolveProxyFallbackTarget(*snapshot, map[int64]service.Proxy{
				backup: {ID: backup, Status: service.StatusActive, ExpiresAt: &future},
			}, now)
			s.Require().True(change)
			updated := *snapshot
			switch edit {
			case "renew":
				updated.ExpiresAt = &future
			case "clear expiry":
				updated.ExpiresAt = nil
			case "disable":
				updated.Status = "inactive"
			case "change mode":
				updated.FallbackMode = service.FallbackModeNone
			case "change backup":
				otherBackup := s.mkProxy("replacement-backup", service.FallbackModeNone, &future, nil)
				updated.BackupProxyID = &otherBackup
			}
			s.Require().NoError(s.repo.Update(s.ctx, &updated))
			var eventsBefore int64
			s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT COUNT(*) FROM scheduler_outbox`, nil, &eventsBefore))
			changed, err := s.repo.sweepOneExpiredProxy(s.ctx, *snapshot, now, target, change)
			s.Require().NoError(err)
			s.Empty(changed)
			got, err := s.repo.GetByID(s.ctx, source)
			s.Require().NoError(err)
			s.Equal(updated.Status, got.Status)
			s.Equal(&source, s.accountProxyID(account))
			var origin *int64
			s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM accounts WHERE id=$1`, []any{account}, &origin))
			s.Nil(origin)
			var eventsAfter int64
			s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT COUNT(*) FROM scheduler_outbox`, nil, &eventsAfter))
			s.Equal(eventsBefore, eventsAfter, "stale sweep must not publish account changes")
		})
	}
}

func (s *ProxyExpirySuite) TestSweep_ChangedModeIsUsedOnNextScan() {
	now := time.Now()
	past := now.Add(-time.Hour)
	source := s.mkProxy("mode-source", service.FallbackModeDirect, &past, nil)
	account := s.mkAccountWithProxy(source)
	snapshot, err := s.repo.GetByID(s.ctx, source)
	s.Require().NoError(err)
	updated := *snapshot
	updated.FallbackMode = service.FallbackModeNone
	s.Require().NoError(s.repo.Update(s.ctx, &updated))
	changed, err := s.repo.sweepOneExpiredProxy(s.ctx, *snapshot, now, nil, true)
	s.Require().NoError(err)
	s.Empty(changed)
	_, err = s.repo.SweepExpiredProxies(s.ctx, now)
	s.Require().NoError(err)
	got, err := s.repo.GetByID(s.ctx, source)
	s.Require().NoError(err)
	s.Equal(service.StatusExpired, got.Status)
	s.Equal(&source, s.accountProxyID(account), "new none policy must preserve account binding")
}

// 备用出口再次到期仍继续回退，人工恢复始终指向第一个出口。
func (s *ProxyExpirySuite) TestSweep_MultipleFallbacksPreserveOriginalProxy() {
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	backup := s.mkProxy("second-exit", service.FallbackModeDirect, &future, nil)
	source := s.mkProxy("first-exit", service.FallbackModeProxy, &past, &backup)
	account := s.mkAccountWithProxy(source)
	_, err := s.repo.SweepExpiredProxies(s.ctx, now)
	s.Require().NoError(err)
	s.Equal(&backup, s.accountProxyID(account))
	_, err = s.tx.ExecContext(s.ctx, `UPDATE proxies SET expires_at=$1 WHERE id=$2`, past, backup)
	s.Require().NoError(err)
	_, err = s.repo.SweepExpiredProxies(s.ctx, now)
	s.Require().NoError(err)
	s.Nil(s.accountProxyID(account))
	var origin *int64
	s.Require().NoError(scanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM accounts WHERE id=$1`, []any{account}, &origin))
	s.Equal(&source, origin)
}
