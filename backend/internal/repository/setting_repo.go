package repository

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/setting"
	"github.com/Wei-Shaw/sub2api/internal/service"
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

func (r *settingRepository) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	now := time.Now()
	err := r.client.Setting.
		Create().
		SetKey(key).
		SetValue(value).
		SetUpdatedAt(now).
		Exec(ctx)
	if err == nil {
		return true, nil
	}
	if ent.IsConstraintError(err) || strings.Contains(strings.ToLower(err.Error()), "constraint") {
		return false, nil
	}
	return false, err
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
	if len(settings) == 0 {
		return nil
	}

	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(settings))
	for key, value := range settings {
		builders = append(builders, r.client.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	return r.client.Setting.
		CreateBulk(builders...).
		OnConflictColumns(setting.FieldKey).
		UpdateNewValues().
		Exec(ctx)
}

// SetMultipleWithLockedRead serializes read/derive/write settings updates on a
// stable settings row. It is used for append-only rule snapshots, where a
// read-then-write performed by two admin requests would otherwise lose one
// snapshot. The callback runs inside the same database transaction while the
// requested rows are locked.
func (r *settingRepository) SetMultipleWithLockedRead(ctx context.Context, updates map[string]string, readKeys []string, mutate func(map[string]string) (map[string]string, error)) error {
	if len(updates) == 0 {
		return nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if len(readKeys) == 0 {
		readKeys = []string{service.SettingKeyDailyCheckinEnabled}
	}
	rows, err := tx.Setting.Query().Where(setting.KeyIn(readKeys...)).ForUpdate().All(ctx)
	if err != nil {
		return err
	}
	current := make(map[string]string, len(rows))
	for _, row := range rows {
		current[row.Key] = row.Value
	}
	if mutate != nil {
		updates, err = mutate(current)
		if err != nil {
			return err
		}
	}
	if len(updates) == 0 {
		return tx.Commit()
	}
	now := time.Now()
	builders := make([]*ent.SettingCreate, 0, len(updates))
	for key, value := range updates {
		builders = append(builders, tx.Setting.Create().SetKey(key).SetValue(value).SetUpdatedAt(now))
	}
	if err := tx.Setting.CreateBulk(builders...).OnConflictColumns(setting.FieldKey).UpdateNewValues().Exec(ctx); err != nil {
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
