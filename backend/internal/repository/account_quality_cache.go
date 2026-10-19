package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

func (c *tempUnschedCache) ReadAccountQuality(ctx context.Context, keys []string) (map[string]service.AccountQualityState, error) {
	out := make(map[string]service.AccountQualityState, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	pipe := c.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.Get(ctx, key)
	}
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for i, cmd := range cmds {
		raw, err := cmd.Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var state service.AccountQualityState
		if err = json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
		out[keys[i]] = state
	}
	return out, nil
}
func (c *tempUnschedCache) RecordAccountQuality(ctx context.Context, key string, sample service.AccountQualitySample) (service.AccountQualityState, error) {
	var out service.AccountQualityState
	for i := 0; i < 32; i++ {
		err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Bytes()
			if err != nil && !errors.Is(err, redis.Nil) {
				return err
			}
			var state service.AccountQualityState
			if len(raw) > 0 {
				if err = json.Unmarshal(raw, &state); err != nil {
					return err
				}
			}
			out = service.AdvanceAccountQualityState(state, sample)
			encoded, err := json.Marshal(out)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, encoded, 24*time.Hour)
				if end := strings.Index(key, "}:"); end >= 0 {
					index := key[:end+2] + "keys"
					pipe.SAdd(ctx, index, key)
					pipe.Expire(ctx, index, 24*time.Hour)
				}
				return nil
			})
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return out, err
		}
	}
	return out, redis.TxFailedErr
}

var qualityPermitScript = redis.NewScript(`
 if redis.call('EXISTS',KEYS[3])==1 then return 0 end
 local now=redis.call('TIME');local ms=tonumber(now[1])*1000+math.floor(tonumber(now[2])/1000)
 local function read_state(key)
  local raw=redis.call('GET',key)
  if not raw then return {} end
  local ok,state=pcall(cjson.decode,raw)
  if not ok then return nil end
  return state
 end
 local state=read_state(KEYS[1]);local account=read_state(KEYS[4])
 if not state or not account then return 0 end
 if tonumber(state.open_until or 0)>ms or tonumber(account.open_until or 0)>ms then return 0 end
 local recovery=tonumber(account.trips or 0)>0
 local probe=ARGV[2]=='1' or tonumber(state.trips or 0)>0 or recovery
 if recovery and not redis.call('SET',KEYS[5],ARGV[1],'NX','EX',900) then return 0 end
 if probe and not redis.call('SET',KEYS[2],ARGV[1],'NX','EX',900) then
  if recovery then redis.call('DEL',KEYS[5]) end
  return 0
 end
 return 1
`)

func (c *tempUnschedCache) AcquireAccountQualityPermit(ctx context.Context, key, id string, probe bool) (bool, error) {
	flag := 0
	if probe {
		flag = 1
	}
	prefix := key[:strings.LastIndex(key, ":")+1]
	n, err := qualityPermitScript.Run(ctx, c.rdb, []string{key, key + ":lease", prefix + "balance_block", prefix + "account", prefix + "account:lease"}, id, flag).Int()
	return n == 1, err
}

var qualityReleaseScript = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`)

func (c *tempUnschedCache) ReleaseAccountQualityPermit(ctx context.Context, key, id string) error {
	return qualityReleaseScript.Run(ctx, c.rdb, []string{key + ":lease"}, id).Err()
}

func (c *tempUnschedCache) ReadAccountQualityAccount(ctx context.Context, id, generation int64) (map[string]service.AccountQualityState, error) {
	index := fmt.Sprintf("account_quality:{%d}:keys", id)
	keys, err := c.rdb.SMembers(ctx, index).Result()
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("account_quality:{%d}:%d:", id, generation)
	values, err := c.ReadAccountQuality(ctx, keys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]service.AccountQualityState)
	expired := make([]any, 0)
	for _, key := range keys {
		if state, ok := values[key]; ok {
			if strings.HasPrefix(key, prefix) {
				out[key] = state
			}
		} else {
			expired = append(expired, key)
		}
	}
	if len(expired) > 0 {
		_ = c.rdb.SRem(ctx, index, expired...).Err()
	}
	return out, nil
}

func (c *tempUnschedCache) SetAccountBalanceBlock(ctx context.Context, id, generation int64) error {
	return c.rdb.Set(ctx, fmt.Sprintf("account_quality:{%d}:%d:balance_block", id, generation), "1", 0).Err()
}

var qualityRenewScript = redis.NewScript(`if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('EXPIRE',KEYS[1],900) end return 0`)

func (c *tempUnschedCache) RenewAccountQualityPermit(ctx context.Context, key, id string) error {
	return qualityRenewScript.Run(ctx, c.rdb, []string{key + ":lease"}, id).Err()
}

func (c *tempUnschedCache) HasAccountBalanceBlock(ctx context.Context, id, generation int64) (bool, error) {
	n, err := c.rdb.Exists(ctx, fmt.Sprintf("account_quality:{%d}:%d:balance_block", id, generation)).Result()
	return n > 0, err
}
