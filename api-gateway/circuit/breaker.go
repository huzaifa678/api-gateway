package circuit

import (
	"context"
	"time"

	"github.com/huzaifa678/SAAS-services/utils"
	"github.com/sony/gobreaker"
)

// NewBreaker builds a configured circuit breaker once
func NewBreaker(name string, cfg utils.CircuitBreakerConfig) *gobreaker.CircuitBreaker {
	return gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:    name,
		Timeout: time.Duration(cfg.TimeoutMs) * time.Millisecond,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures >= uint32(cfg.ErrorThreshold)
		},
		Interval: time.Duration(cfg.ResetTimeoutMs) * time.Millisecond,
	})
}

func WrapWithBreaker(fn func(ctx context.Context) (interface{}, error), name string, cfg utils.CircuitBreakerConfig) func(ctx context.Context) (interface{}, error) {
	cb := NewBreaker(name, cfg)

	return func(ctx context.Context) (interface{}, error) {
		res, err := cb.Execute(func() (interface{}, error) {
			r, e := fn(ctx)
			if e != nil {
				return nil, e
			}
			return r, nil
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	}
}
