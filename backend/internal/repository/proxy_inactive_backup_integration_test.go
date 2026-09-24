//go:build integration

package repository

import (
	"github.com/TokenFlux/TokenRouter/internal/service"
	"time"
)

func (s *ProxyExpirySuite) TestSweep_SkipsInactiveBackup() {
	for _, chain := range []bool{false, true} {
		s.Run(map[bool]string{false: "unresolved", true: "next active backup"}[chain], func() {
			now := time.Now()
			past := now.Add(-time.Hour)
			future := now.Add(time.Hour)
			healthy := s.mkProxy("active-tail", service.FallbackModeNone, &future, nil)
			backup := s.mkProxy("disabled-backup", service.FallbackModeNone, &future, nil)
			disabled, err := s.repo.GetByID(s.ctx, backup)
			s.Require().NoError(err)
			disabled.Status = "inactive"
			if chain {
				disabled.FallbackMode = service.FallbackModeProxy
				disabled.BackupProxyID = &healthy
			}
			s.Require().NoError(s.repo.Update(s.ctx, disabled))
			source := s.mkProxy("expired-source", service.FallbackModeProxy, &past, &backup)
			account := s.mkAccountWithProxy(source)
			_, err = s.repo.SweepExpiredProxies(s.ctx, now)
			s.Require().NoError(err)
			if chain {
				s.Equal(&healthy, s.accountProxyID(account))
			} else {
				s.Equal(&source, s.accountProxyID(account))
			}
			got, err := s.repo.GetByID(s.ctx, backup)
			s.Require().NoError(err)
			s.Equal("inactive", got.Status, "fallback traversal must not reactivate disabled proxies")
		})
	}
}

// 使用产品仓储创建及调整链路，确认共享目标、清空与删除不会反写其他代理。
func (s *ProxyExpirySuite) TestConfigureFallbackChainAndSharedTarget() {
	future := time.Now().Add(time.Hour)
	tail := s.mkProxy("tail", service.FallbackModeNone, &future, nil)
	middle := s.mkProxy("middle", service.FallbackModeProxy, &future, &tail)
	first := s.mkProxy("first", service.FallbackModeProxy, &future, &middle)
	peer := s.mkProxy("peer", service.FallbackModeProxy, &future, &tail)
	assertBackup := func(id int64, want *int64) {
		got, err := s.repo.GetByID(s.ctx, id)
		s.Require().NoError(err)
		s.Equal(want, got.BackupProxyID)
	}
	assertBackup(tail, nil)
	assertBackup(middle, &tail)
	assertBackup(first, &middle)
	assertBackup(peer, &tail)
	updated, err := s.repo.GetByID(s.ctx, peer)
	s.Require().NoError(err)
	updated.BackupProxyID = &middle
	s.Require().NoError(s.repo.Update(s.ctx, updated))
	assertBackup(middle, &tail)
	assertBackup(peer, &middle)
	assertBackup(first, &middle)
	cleared, err := s.repo.GetByID(s.ctx, first)
	s.Require().NoError(err)
	cleared.BackupProxyID = nil
	cleared.FallbackMode = service.FallbackModeNone
	s.Require().NoError(s.repo.Update(s.ctx, cleared))
	assertBackup(first, nil)
	assertBackup(middle, &tail)
	assertBackup(peer, &middle)
	s.Require().NoError(s.repo.Delete(s.ctx, peer))
	_, err = s.repo.GetByID(s.ctx, peer)
	s.Require().ErrorIs(err, service.ErrProxyNotFound)
	assertBackup(middle, &tail)
	assertBackup(tail, nil)
}
