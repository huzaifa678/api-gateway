package endpoint

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kit/kit/endpoint"
	kitlog "github.com/go-kit/log"
	"github.com/huzaifa678/SAAS-services/throttling"
	"github.com/redis/go-redis/v9"
	"github.com/huzaifa678/SAAS-services/errors"
)

type RedisRateLimiter struct {
	redisClient *redis.Client
	rps         int           
	burst       int           
	keyPrefix   string
	logger      kitlog.Logger
	maxMemoryUsage float64
	ttl           time.Duration
}

func RateLimitMiddleware(redisClient *redis.Client, rps int, burst int, keyPrefix string, logger kitlog.Logger, ttl time.Duration,) endpoint.Middleware {
	limiter := &RedisRateLimiter{
		redisClient: redisClient,
		rps:         rps,
		burst:       burst,
		keyPrefix:   keyPrefix,
		logger:      logger,
		maxMemoryUsage: 0.8, // 80% memory usage threshold
		ttl: ttl,
	}

	return func(next endpoint.Endpoint) endpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (interface{}, error) {
			userID, _ := ctx.Value("userId").(string)
			if userID == "" {
				userID = "anonymous"
			}

			pressure, err := throttling.IsStorageUnderPressure(ctx, limiter.redisClient, limiter.maxMemoryUsage)
			if err != nil {
				_ = limiter.logger.Log(
					"msg", "storage check failed",
					"err", err,
				)
			}

			if pressure {
				_ = limiter.logger.Log(
					"msg", "storage under pressure - throttling",
					"userID", userID,
				)

				return nil, errors.ErrStoragePressure
			}

			key := fmt.Sprintf("%s:%s", limiter.keyPrefix, userID)
			allowed, err := limiter.Allow(ctx, key)
			if err != nil {
				_ = limiter.logger.Log(
					"msg", "rate limiter error",
					"userID", userID,
					"key", key,
					"err", err,
				)
				return nil, err
			}

			if !allowed {
				_ = limiter.logger.Log(
					"msg", "rate limit exceeded",
					"userID", userID,
					"key", key,
					"rps", limiter.rps,
					"burst", limiter.burst,
				)
				return nil, errors.ErrRateLimitExceeded
			}

			_ = limiter.logger.Log(
				"msg", "rate limit allowed",
				"userID", userID,
				"key", key,
			)

			return next(ctx, request)
		}
	}
}

func (r *RedisRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	
	script := `
	local tokens_key = KEYS[1]
	local ts_key = KEYS[2]

	local rate = tonumber(ARGV[1])
	local capacity = tonumber(ARGV[2])
	local now = tonumber(ARGV[3])
	local requested = tonumber(ARGV[4])
	local ttl = tonumber(ARGV[5])

	local tokens = tonumber(redis.call("GET", tokens_key))
	if tokens == nil then
		tokens = capacity
	end

	local last_ts = tonumber(redis.call("GET", ts_key))
	if last_ts == nil then
		last_ts = now
	end

	local delta = math.max(0, now - last_ts)

	local refill = delta * rate
	tokens = math.min(capacity, tokens + refill)

	local allowed = tokens >= requested

	if allowed then
		tokens = tokens - requested
	end

	-- persisting updated values
	redis.call("SET", tokens_key, tokens)
	redis.call("SET", ts_key, now)

	redis.call("EXPIRE", tokens_key, ttl)
	redis.call("EXPIRE", ts_key, ttl)

	if allowed then
		return 1
	else
		return 0
	end
	`

	now := float64(time.Now().UnixNano()) / 1e9

	res, err := r.redisClient.Eval(
		ctx,
		script,
		[]string{key + ":tokens", key + ":ts"},
		r.rps,
		r.burst,
		now,
		1,
		int(r.ttl.Seconds()), 
	).Int()

	if err != nil {
		return false, err
	}

	return res == 1, nil
}