package repository

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/setting"
	"github.com/TokenFlux/TokenRouter/internal/service"
)

type settingRepository struct {
	client *ent.Client
}

func NewSettingRepository(client *ent.Client) service.SettingRepository {
	return &settingRepository{client: client}
}

func (r *settingRepository) Get(ctx context.Context, key string) (*service.Setting, error) {
	m, err := r.client.Setting.Query().Where(setting.KeyEQ(key)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, service.ErrSettingNotFound
		}
		return nil, err
	}
	return &service.Setting{
		ID:        m.ID,
		Key:       m.Key,
		Value:     m.Value,
		UpdatedAt: m.UpdatedAt,
	}, nil
}

func (r *settingRepository) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *settingRepository) Set(ctx context.Context, key, value string) error {
	now := time.Now()
	return r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

func (r *settingRepository) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	if len(keys) == 0 {
		return map[string]string{}, nil
	}
	settings, err := r.client.Setting.Query().Where(setting.KeyIn(keys...)).All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) SetMultiple(ctx context.Context, settings map[string]string) error {
	return setMultipleSettings(ctx, r.client, settings)
}

// setMultipleSettings 共用客户端和事务写入，按键排序避免并发批量更新的反向锁顺序。
func setMultipleSettings(ctx context.Context, client *ent.Client, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builders = append(builders, client.Setting.Create().SetKey(key).SetValue(settings[key]).SetUpdatedAt(now))
	}
	return client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

// UpdateMultiple 先为缺省键建立空值行，再按固定顺序持有行锁完成读、合并、校验和写入。
// 行锁由数据库事务持有，多个服务实例也不能同时依据旧的优惠配置提交非法组合。
func (r *settingRepository) UpdateMultiple(ctx context.Context, keys []string, update func(map[string]string) (map[string]string, error)) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin settings update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	keys = append([]string(nil), keys...)
	sort.Strings(keys)
	stored := make(map[string]string, len(keys))
	for _, key := range keys {
		if _, ok := stored[key]; ok {
			continue
		}
		if err := tx.Setting.Create().SetKey(key).SetValue("").
			OnConflictColumns(setting.FieldKey).Ignore().Exec(ctx); err != nil {
			return fmt.Errorf("initialize settings update key: %w", err)
		}
		row, err := tx.Setting.Query().Where(setting.KeyEQ(key)).ForUpdate().Only(ctx)
		if err != nil {
			return fmt.Errorf("lock settings update key: %w", err)
		}
		stored[key] = row.Value
	}
	updates, err := update(stored)
	if err != nil {
		return err
	}
	if err := setMultipleSettings(ctx, tx.Client(), updates); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *settingRepository) GetAll(ctx context.Context) (map[string]string, error) {
	settings, err := r.client.Setting.Query().All(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]string)
	for _, s := range settings {
		result[s.Key] = s.Value
	}
	return result, nil
}

func (r *settingRepository) Delete(ctx context.Context, key string) error {
	_, err := r.client.Setting.Delete().Where(setting.KeyEQ(key)).Exec(ctx)
	return err
}
