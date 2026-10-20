//go:build unit

package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/config"
	"github.com/stretchr/testify/require"
)

// 使用认证加密模拟跨进程共享密钥，避免可逆字符串替身掩盖快照篡改问题。
type cleanupTestEncryptor struct{ cipher.AEAD }

func newCleanupTestEncryptor(t *testing.T) cleanupTestEncryptor {
	t.Helper()
	block, err := aes.NewCipher([]byte("retention-test-key-32-bytes-long"))
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)
	return cleanupTestEncryptor{aead}
}

func (e cleanupTestEncryptor) Encrypt(plain string) (string, error) {
	nonce := make([]byte, e.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(e.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func (e cleanupTestEncryptor) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) < e.NonceSize() {
		return "", errors.New("invalid test ciphertext")
	}
	plain, err := e.Open(nil, data[:e.NonceSize()], data[e.NonceSize():], nil)
	return string(plain), err
}

// 队列替身保留首次登记内容，并在失败后留存记录，以验证业务生命周期。
type cleanupTestStore struct {
	mu            sync.Mutex
	records       map[string]ImageObjectCleanupRecord
	nextAttempt   map[string]time.Time
	leased        map[string]bool
	checkErr      error
	scheduleErr   error
	completeErr   error
	retryErr      error
	scheduleCalls int
	completeCalls int
	retryCodes    []string
	canonical     time.Time
	forceClaim    bool
}

func newCleanupTestStore() *cleanupTestStore {
	return &cleanupTestStore{
		records: make(map[string]ImageObjectCleanupRecord), nextAttempt: make(map[string]time.Time),
		leased: make(map[string]bool),
	}
}

func (s *cleanupTestStore) Check(context.Context) error { return s.checkErr }

func (s *cleanupTestStore) Schedule(_ context.Context, record ImageObjectCleanupRecord) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleCalls++
	if s.scheduleErr != nil {
		return time.Time{}, s.scheduleErr
	}
	if existing, ok := s.records[record.ID]; ok {
		return existing.ExpiresAt, nil
	}
	if !s.canonical.IsZero() {
		record.ExpiresAt = s.canonical
	}
	s.records[record.ID] = record
	return record.ExpiresAt, nil
}

func (s *cleanupTestStore) ClaimDue(_ context.Context, now time.Time, _ int, _ time.Duration) ([]ImageObjectCleanupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, record := range s.records {
		if s.leased[id] || s.nextAttempt[id].After(now) || (!s.forceClaim && record.ExpiresAt.After(now)) {
			continue
		}
		record.LeaseToken = "lease-" + id
		record.Attempts++
		s.records[id], s.leased[id] = record, true
		return []ImageObjectCleanupRecord{record}, nil
	}
	return nil, nil
}

func (s *cleanupTestStore) Complete(_ context.Context, id, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	if s.completeErr != nil {
		return s.completeErr
	}
	delete(s.records, id)
	return nil
}

func (s *cleanupTestStore) Retry(_ context.Context, id, _ string, next time.Time, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryCodes = append(s.retryCodes, code)
	if s.retryErr != nil {
		return s.retryErr
	}
	s.nextAttempt[id], s.leased[id] = next, false
	return nil
}

func (s *cleanupTestStore) saved() []ImageObjectCleanupRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ImageObjectCleanupRecord, 0, len(s.records))
	for _, record := range s.records {
		result = append(result, record)
	}
	return result
}

type cleanupTestUpload struct {
	key, contentType string
	data             []byte
	expiry           time.Time
}

type cleanupTestStorage struct {
	saveCalls int
	uploads   []cleanupTestUpload
	deleted   []string
	saveErr   error
	deleteErr error
	readyErr  error
	beforePut func()
	onDelete  func(context.Context, string) error
}

func (s *cleanupTestStorage) Save(context.Context, string, string, []byte) (string, error) {
	s.saveCalls++
	return "", errors.New("ordinary Save must not bypass absolute expiry")
}

func (s *cleanupTestStorage) SaveUntil(_ context.Context, key, contentType string, data []byte, expiry time.Time) (string, error) {
	if s.beforePut != nil {
		s.beforePut()
	}
	s.uploads = append(s.uploads, cleanupTestUpload{key, contentType, append([]byte(nil), data...), expiry})
	return "https://storage.example.test/" + key, s.saveErr
}

func (s *cleanupTestStorage) CheckReadiness(context.Context) error { return s.readyErr }

func (s *cleanupTestStorage) Delete(ctx context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	if s.onDelete != nil {
		return s.onDelete(ctx, key)
	}
	return s.deleteErr
}

func cleanupTestConfig() config.ImageStorageConfig {
	return config.ImageStorageConfig{
		Enabled: true, Endpoint: "https://old-account.example.test", Region: "auto",
		Bucket: "old-bucket", Prefix: "images/", AccessKeyID: "old-access",
		SecretAccessKey: "old-secret", PresignExpiry: 12,
	}
}

const cleanupTestObjectKey = "images/imgtask_0123456789abcdef0123456789abcdef-0.png"

func cleanupTestManager(t *testing.T) (*ImageObjectRetention, *cleanupTestStore, *cleanupTestStorage) {
	t.Helper()
	store, storage := newCleanupTestStore(), &cleanupTestStorage{}
	manager := NewImageObjectRetention(store, newCleanupTestEncryptor(t),
		func(context.Context, *config.ImageStorageConfig) (ImageStorage, error) { return storage, nil }, true)
	return manager, store, storage
}

func TestImageObjectRetentionRegistersBeforePutAndKeepsOriginalExpiry(t *testing.T) {
	manager, store, storage := cleanupTestManager(t)
	cfg := cleanupTestConfig()
	wrapped := manager.Wrap(&cfg, storage)
	storage.beforePut = func() {
		records := store.saved()
		require.Len(t, records, 1, "PUT 前必须已有持久清理记录")
		require.Equal(t, cleanupTestObjectKey, records[0].ObjectKey)
	}
	before := time.Now().UTC().Truncate(time.Second)
	storage.saveErr = errors.New("transport lost after PUT")
	_, err := wrapped.Save(context.Background(), cleanupTestObjectKey, "image/png", []byte("image"))
	require.Error(t, err)
	original := store.saved()[0]
	require.WithinRange(t, original.ExpiresAt, before.Add(24*time.Hour), time.Now().UTC().Add(24*time.Hour))
	require.NotContains(t, original.StorageCiphertext, cfg.SecretAccessKey)
	require.NotContains(t, original.StorageCiphertext, cfg.Bucket)

	// 同对象重传即使新配置改为 72 小时，仍须使用首次登记的期限和密文。
	cfg.RetentionHours = 72
	storage.saveErr = nil
	_, err = manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", []byte("image"))
	require.NoError(t, err)
	require.Equal(t, original, store.saved()[0])
	require.Len(t, storage.uploads, 2)
	require.Equal(t, original.ExpiresAt, storage.uploads[0].expiry)
	require.Equal(t, original.ExpiresAt, storage.uploads[1].expiry)
	require.Equal(t, []byte("image"), storage.uploads[1].data)
	require.Equal(t, "image/png", storage.uploads[1].contentType)
	require.Zero(t, storage.saveCalls)
}

func TestImageObjectRetentionCustomAndCanonicalDeadline(t *testing.T) {
	for _, hours := range []int{1, 72, 8760} {
		t.Run(time.Duration(hours).String(), func(t *testing.T) {
			manager, store, storage := cleanupTestManager(t)
			cfg := cleanupTestConfig()
			cfg.RetentionHours = hours
			before := time.Now().UTC().Truncate(time.Second)
			_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
			require.NoError(t, err)
			require.WithinRange(t, store.saved()[0].ExpiresAt, before.Add(time.Duration(hours)*time.Hour), time.Now().UTC().Add(time.Duration(hours)*time.Hour))
		})
	}
	t.Run("canonical expiry from concurrent registration", func(t *testing.T) {
		manager, store, storage := cleanupTestManager(t)
		store.canonical = time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
		cfg := cleanupTestConfig()
		_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
		require.NoError(t, err)
		require.Equal(t, store.canonical, storage.uploads[0].expiry)
	})
	t.Run("expired registration cannot upload again", func(t *testing.T) {
		manager, store, storage := cleanupTestManager(t)
		store.canonical = time.Now().Add(-time.Second)
		cfg := cleanupTestConfig()
		_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
		require.ErrorContains(t, err, "deadline has passed")
		require.Empty(t, storage.uploads)
	})
}

func TestImageObjectRetentionRegistrationFailureNeverPuts(t *testing.T) {
	manager, store, storage := cleanupTestManager(t)
	store.scheduleErr = errors.New("database unavailable")
	cfg := cleanupTestConfig()
	_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
	require.ErrorContains(t, err, "registration failed")
	require.Empty(t, storage.uploads)
	require.Zero(t, storage.saveCalls)
	require.Empty(t, store.saved())
}

func TestImageObjectRetentionReadinessGuardsUploads(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ImageObjectRetention, *cleanupTestStore)
	}{
		{"unstable encryption key", func(m *ImageObjectRetention, _ *cleanupTestStore) { m.stableEncryption = false }},
		{"missing encryption", func(m *ImageObjectRetention, _ *cleanupTestStore) { m.encryptor = nil }},
		{"missing factory", func(m *ImageObjectRetention, _ *cleanupTestStore) { m.factory = nil }},
		{"missing queue", func(m *ImageObjectRetention, _ *cleanupTestStore) { m.store = nil }},
		{"queue not ready", func(_ *ImageObjectRetention, s *cleanupTestStore) { s.checkErr = errors.New("migration missing") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, store, storage := cleanupTestManager(t)
			tc.edit(manager, store)
			cfg := cleanupTestConfig()
			wrapped := manager.Wrap(&cfg, storage).(*retainedImageStorage)
			require.Error(t, wrapped.CheckReadiness(context.Background()))
			_, err := wrapped.Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
			require.Error(t, err)
			require.Empty(t, storage.uploads)
			require.Zero(t, store.scheduleCalls)
		})
	}
	t.Run("storage probe errors propagate", func(t *testing.T) {
		manager, _, storage := cleanupTestManager(t)
		storage.readyErr = errors.New("bucket denied")
		cfg := cleanupTestConfig()
		require.ErrorIs(t, manager.Wrap(&cfg, storage).(*retainedImageStorage).CheckReadiness(context.Background()), storage.readyErr)
		require.Empty(t, storage.uploads)
	})
	t.Run("legacy storage cannot bypass retention", func(t *testing.T) {
		manager, _, _ := cleanupTestManager(t)
		storage := &recordingStorage{}
		cfg := cleanupTestConfig()
		wrapped := manager.Wrap(&cfg, storage).(*retainedImageStorage)
		require.Error(t, wrapped.CheckReadiness(context.Background()))
		_, err := wrapped.Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
		require.ErrorContains(t, err, "absolute retention deadline")
		require.Empty(t, storage.saved)
	})
}

func TestImageObjectRetentionRestartAndConfigurationChangeUseOriginalBucket(t *testing.T) {
	manager, store, storage := cleanupTestManager(t)
	cfg := cleanupTestConfig()
	_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
	require.NoError(t, err)
	record := store.saved()[0]
	require.NoError(t, manager.Sweep(context.Background(), record.ExpiresAt.Add(-time.Hour)))
	require.Empty(t, storage.deleted)

	// 重启后只依赖持久队列和同一稳定密钥，不能使用当前新桶覆盖旧对象位置。
	var recovered []config.ImageStorageConfig
	oldBucketStorage := &cleanupTestStorage{}
	restarted := NewImageObjectRetention(store, newCleanupTestEncryptor(t), func(_ context.Context, cfg *config.ImageStorageConfig) (ImageStorage, error) {
		recovered = append(recovered, *cfg)
		return oldBucketStorage, nil
	}, true)
	cfg.Endpoint, cfg.Bucket, cfg.SecretAccessKey, cfg.Enabled = "https://new.example.test", "new-bucket", "new-secret", false
	_ = restarted.Wrap(&cfg, &cleanupTestStorage{})
	require.NoError(t, restarted.Sweep(context.Background(), record.ExpiresAt.Add(time.Second)))
	require.Equal(t, []config.ImageStorageConfig{cleanupTestConfig()}, recovered)
	require.Equal(t, []string{cleanupTestObjectKey}, oldBucketStorage.deleted)
	require.Empty(t, storage.deleted)
	require.Empty(t, store.saved())
}

func TestImageObjectRetentionRejectsTamperedAndPrematureRecords(t *testing.T) {
	cases := []struct {
		name string
		edit func(*testing.T, *ImageObjectRetention, *ImageObjectCleanupRecord)
		code string
	}{
		{"future expiry", func(_ *testing.T, _ *ImageObjectRetention, _ *ImageObjectCleanupRecord) {}, "cleanup_snapshot_invalid"},
		{"changed row key", func(_ *testing.T, _ *ImageObjectRetention, r *ImageObjectCleanupRecord) {
			r.ObjectKey = "backups/database.sql"
		}, "cleanup_snapshot_invalid"},
		{"changed row id", func(_ *testing.T, _ *ImageObjectRetention, r *ImageObjectCleanupRecord) { r.ID = "another-record" }, "cleanup_snapshot_invalid"},
		{"changed row expiry", func(_ *testing.T, _ *ImageObjectRetention, r *ImageObjectCleanupRecord) {
			r.ExpiresAt = r.ExpiresAt.Add(-time.Hour)
		}, "cleanup_snapshot_invalid"},
		{"corrupted ciphertext", func(_ *testing.T, _ *ImageObjectRetention, r *ImageObjectCleanupRecord) {
			r.StorageCiphertext = "not encrypted"
		}, "cleanup_decrypt_failed"},
		{"authenticated malformed json", func(t *testing.T, m *ImageObjectRetention, r *ImageObjectCleanupRecord) {
			var err error
			r.StorageCiphertext, err = m.encryptor.Encrypt("{")
			require.NoError(t, err)
		}, "cleanup_snapshot_invalid"},
		{"unowned object even with coherent snapshot", func(t *testing.T, m *ImageObjectRetention, r *ImageObjectCleanupRecord) {
			cfg := cleanupTestConfig()
			r.ObjectKey = "images/../backup.sql"
			r.ID = imageObjectCleanupID(&cfg, r.ObjectKey)
			plain, err := json.Marshal(imageObjectCleanupSnapshot{Storage: cfg, ObjectKey: r.ObjectKey, ExpiresAt: r.ExpiresAt})
			require.NoError(t, err)
			r.StorageCiphertext, err = m.encryptor.Encrypt(string(plain))
			require.NoError(t, err)
		}, "cleanup_snapshot_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager, store, storage := cleanupTestManager(t)
			cfg := cleanupTestConfig()
			_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
			require.NoError(t, err)
			record := store.saved()[0]
			now := record.ExpiresAt.Add(time.Second)
			if tc.name == "future expiry" {
				now = record.ExpiresAt.Add(-time.Hour)
			}
			tc.edit(t, manager, &record)
			store.records = map[string]ImageObjectCleanupRecord{record.ID: record}
			store.forceClaim = true
			require.NoError(t, manager.Sweep(context.Background(), now))
			require.Empty(t, storage.deleted, "任何证据不一致都不能删除对象")
			require.Zero(t, store.completeCalls)
			require.Equal(t, []string{tc.code}, store.retryCodes)
			require.Len(t, store.saved(), 1)
		})
	}
}

func TestImageObjectRetentionCleanupFailureKeepsEvidenceAndRetries(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*cleanupTestStore, *cleanupTestStorage)
		code string
	}{
		{"delete failed", func(_ *cleanupTestStore, s *cleanupTestStorage) { s.deleteErr = errors.New("temporary failure") }, "cleanup_delete_failed"},
		{"delete timed out", func(_ *cleanupTestStore, s *cleanupTestStorage) { s.deleteErr = context.DeadlineExceeded }, "cleanup_timeout"},
		{"completion failed", func(s *cleanupTestStore, _ *cleanupTestStorage) { s.completeErr = errors.New("database lost") }, "cleanup_complete_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, store, storage := cleanupTestManager(t)
			cfg := cleanupTestConfig()
			_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
			require.NoError(t, err)
			record := store.saved()[0]
			tc.edit(store, storage)
			now := record.ExpiresAt.Add(time.Second)
			require.NoError(t, manager.Sweep(context.Background(), now))
			require.Len(t, store.saved(), 1)
			require.Equal(t, record.StorageCiphertext, store.saved()[0].StorageCiphertext)
			require.Equal(t, []string{tc.code}, store.retryCodes)
			require.GreaterOrEqual(t, store.nextAttempt[record.ID].Sub(now), time.Minute)
			require.NoError(t, manager.Sweep(context.Background(), now.Add(30*time.Second)))
			require.Len(t, storage.deleted, 1, "退避期间不重复删除")
			store.completeErr, storage.deleteErr = nil, nil
			require.NoError(t, manager.Sweep(context.Background(), now.Add(2*time.Minute)))
			require.Empty(t, store.saved())
			require.Len(t, storage.deleted, 2, "完成写入失败后允许幂等重删同一对象")
			require.Equal(t, storage.deleted[0], storage.deleted[1])
		})
	}
	t.Run("retry persistence failed", func(t *testing.T) {
		manager, store, storage := cleanupTestManager(t)
		cfg := cleanupTestConfig()
		_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
		require.NoError(t, err)
		storage.deleteErr, store.retryErr = errors.New("storage failure"), errors.New("queue failure")
		require.ErrorContains(t, manager.Sweep(context.Background(), store.saved()[0].ExpiresAt.Add(time.Second)), "retry persistence failed")
		require.Len(t, store.saved(), 1)
	})
	t.Run("completion lease lost", func(t *testing.T) {
		manager, store, storage := cleanupTestManager(t)
		cfg := cleanupTestConfig()
		_, err := manager.Wrap(&cfg, storage).Save(context.Background(), cleanupTestObjectKey, "image/png", nil)
		require.NoError(t, err)
		store.completeErr = ErrImageObjectCleanupLeaseLost
		require.NoError(t, manager.Sweep(context.Background(), store.saved()[0].ExpiresAt.Add(time.Second)))
		require.Len(t, store.saved(), 1)
		require.Empty(t, store.retryCodes, "不能覆盖另一持有者的租约状态")
	})
}
