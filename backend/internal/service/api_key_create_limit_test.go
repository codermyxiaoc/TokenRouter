//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 并发替身只提供原子计数；真实 Redis 窗口另由仓储层并发测试验证。
type atomicAPIKeyCreationCache struct {
	apiKeyCacheStub
	created atomic.Int64
	failed  atomic.Int64
}

func (s *atomicAPIKeyCreationCache) IncrementCreateCount(context.Context, int64, time.Duration) (int64, error) {
	return s.created.Add(1), nil
}
func (s *atomicAPIKeyCreationCache) GetCreateAttemptCount(context.Context, int64) (int, error) {
	return int(s.failed.Load()), nil
}
func (s *atomicAPIKeyCreationCache) IncrementCreateAttemptCount(context.Context, int64) error {
	s.failed.Add(1)
	return nil
}
func (s *atomicAPIKeyCreationCache) DeleteCreateAttemptCount(context.Context, int64) error {
	s.failed.Store(0)
	return nil
}
func (s *atomicAPIKeyCreationCache) DeleteAuthCache(context.Context, string) error { return nil }

type concurrentAPIKeyCreationRepo struct {
	apiKeyNameSanitizeRepoStub
	inserted  atomic.Int64
	duplicate bool
}

// 现有用户仓储替身会追加查询记录；并发测试使用无共享写入的独立替身。
type concurrentAPIKeyCreationUserRepo struct{ UserRepository }

func (concurrentAPIKeyCreationUserRepo) GetByID(_ context.Context, id int64) (*User, error) {
	return &User{ID: id, Status: StatusActive}, nil
}

func (r *concurrentAPIKeyCreationRepo) Create(_ context.Context, key *APIKey) error {
	key.ID = r.inserted.Add(1)
	return nil
}
func (r *concurrentAPIKeyCreationRepo) ExistsByKey(context.Context, string) (bool, error) {
	return r.duplicate, nil
}
func (r *concurrentAPIKeyCreationRepo) DeleteWithAudit(context.Context, int64) error { return nil }

// 穿过 Create 的随机与自定义分支验证：并发超限必须在数据库插入之前拒绝。
func TestAPIKeyCreateLimitConcurrentCreateOnlyPersistsSixty(t *testing.T) {
	cache := &atomicAPIKeyCreationCache{}
	repo := &concurrentAPIKeyCreationRepo{}
	svc := NewAPIKeyService(repo, concurrentAPIKeyCreationUserRepo{}, nil, nil, nil, cache,
		&config.Config{APIKeyCreate: config.APIKeyCreateConfig{MaxPerUserPerHour: 60}})
	results := make([]error, 120)
	var workers sync.WaitGroup
	for i := range results {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			req := CreateAPIKeyRequest{Name: "concurrent"}
			if i%2 == 0 {
				custom := fmt.Sprintf("sk_concurrent_create_%03d", i)
				req.CustomKey = &custom
			}
			_, results[i] = svc.Create(context.Background(), 7, req)
		}(i)
	}
	workers.Wait()
	created, rejected := 0, 0
	for _, err := range results {
		if err == nil {
			created++
		} else {
			require.ErrorIs(t, err, ErrAPIKeyCreateLimited)
			rejected++
		}
	}
	require.Equal(t, 60, created)
	require.Equal(t, 60, rejected)
	require.Equal(t, int64(60), repo.inserted.Load())
	require.Zero(t, cache.failed.Load())
}

// 删除已有 Key 和自定义 Key 猜测失败都不能返还或串用创建窗口。
func TestAPIKeyCreateLimitDeletionAndDuplicateAttemptsStayIndependent(t *testing.T) {
	cache := &atomicAPIKeyCreationCache{}
	repo := &concurrentAPIKeyCreationRepo{}
	svc := NewAPIKeyService(repo, &billingModeUserRepoStub{users: map[int64]*User{
		7: {ID: 7, Status: StatusActive},
	}}, nil, nil, nil, cache, &config.Config{APIKeyCreate: config.APIKeyCreateConfig{MaxPerUserPerHour: 1}})
	key, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "first"})
	require.NoError(t, err)
	repo.apiKey = key
	repo.duplicate = true
	custom := "sk_existing_key_collision"
	_, err = svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "duplicate", CustomKey: &custom})
	require.ErrorIs(t, err, ErrAPIKeyExists)
	require.Equal(t, int64(1), cache.failed.Load())
	require.Equal(t, int64(1), cache.created.Load())
	require.NoError(t, svc.Delete(context.Background(), key.ID, 7))
	require.Equal(t, int64(1), cache.failed.Load())
	require.Equal(t, int64(1), cache.created.Load())
	_, err = svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "after-delete"})
	require.ErrorIs(t, err, ErrAPIKeyCreateLimited)
	require.Equal(t, int64(1), repo.inserted.Load())
}

// 创建次数与自定义密钥错误次数使用不同方法，确保删除和团队付款身份不会重置或串用窗口。
type apiKeyCreationCounterStub struct {
	apiKeyCacheStub
	count int64
	err   error
	users []int64
}

func (s *apiKeyCreationCounterStub) IncrementCreateCount(_ context.Context, userID int64, window time.Duration) (int64, error) {
	if window != time.Hour {
		panic("unexpected creation window")
	}
	s.users = append(s.users, userID)
	s.count++
	return s.count, s.err
}

func TestAPIKeyCreateLimitCoversGeneratedCustomAndTeamKeys(t *testing.T) {
	cache := &apiKeyCreationCounterStub{}
	repo := &apiKeyNameSanitizeRepoStub{}
	svc := NewAPIKeyService(repo, &billingModeUserRepoStub{users: map[int64]*User{
		7: {ID: 7, Status: StatusActive}, 8: {ID: 8, Status: StatusActive},
	}}, nil, nil, nil, cache, &config.Config{
		APIKeyCreate: config.APIKeyCreateConfig{MaxPerUserPerHour: 2},
		Team:         config.TeamConfig{Enabled: true},
	})
	svc.SetTeamRepository(&fakeTeamRepository{teamContext: &TeamContext{
		Team:       &Team{ID: 11, Status: TeamStatusActive},
		Membership: &TeamMembership{TeamID: 11, UserID: 7, Role: TeamRoleMember},
		Owner:      &TeamMembership{TeamID: 11, UserID: 8, Role: TeamRoleOwner},
	}})
	_, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "personal"})
	require.NoError(t, err)
	custom := "sk_team_creation_window"
	key, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "team", Scope: "team", CustomKey: &custom})
	require.NoError(t, err)
	require.Equal(t, int64(8), key.User.ID)
	_, err = svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "over limit"})
	require.ErrorIs(t, err, ErrAPIKeyCreateLimited)
	require.Equal(t, []int64{7, 7, 7}, cache.users)
	require.Len(t, repo.created, 2)
}

func TestAPIKeyCreateLimitDisabledAndRedisFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limit    int
		cacheErr error
		calls    int
	}{
		{"关闭", 0, nil, 0}, {"缓存故障放行", 1, errors.New("redis unavailable"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &apiKeyCreationCounterStub{count: 10, err: tc.cacheErr}
			svc := &APIKeyService{cache: cache, cfg: &config.Config{APIKeyCreate: config.APIKeyCreateConfig{MaxPerUserPerHour: tc.limit}}}
			require.NoError(t, svc.checkAPIKeyCreateLimit(context.Background(), 7))
			require.Len(t, cache.users, tc.calls)
		})
	}
}
