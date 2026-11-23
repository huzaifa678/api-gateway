package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisIdemStore is the Redis-backed idemStore.
type redisIdemStore struct{ rdb *redis.Client }

func (s *redisIdemStore) Result(ctx context.Context, key string) (idemResult, bool, error) {
	raw, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return idemResult{}, false, nil
	}
	if err != nil {
		return idemResult{}, false, err
	}
	var res idemResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return idemResult{}, false, err
	}
	return res, true, nil
}

func (s *redisIdemStore) Acquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, key, token, ttl).Result()
}

func (s *redisIdemStore) Save(ctx context.Context, key string, res idemResult, ttl time.Duration) error {
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, key, raw, ttl).Err()
}

// releaseLock drops the lock only if we still hold it (token match).
var releaseLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0`)

func (s *redisIdemStore) Release(ctx context.Context, key, token string) error {
	return releaseLock.Run(ctx, s.rdb, []string{key}, token).Err()
}
