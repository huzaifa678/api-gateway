package circuit

import (
	"context"
	"errors"
	"testing"

	"github.com/huzaifa678/SAAS-services/utils"
)

var testCfg = utils.CircuitBreakerConfig{
	TimeoutMs:      1000,
	ErrorThreshold: 3,
	ResetTimeoutMs: 5000,
}

func TestWrapWithBreaker_Success(t *testing.T) {
	fn := func(ctx context.Context) (interface{}, error) {
		return "ok", nil
	}
	wrapped := WrapWithBreaker(fn, "test-svc", testCfg)
	res, err := wrapped(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "ok" {
		t.Fatalf("expected 'ok', got %v", res)
	}
}

func TestWrapWithBreaker_PropagatesError(t *testing.T) {
	fn := func(ctx context.Context) (interface{}, error) {
		return nil, errors.New("downstream error")
	}
	wrapped := WrapWithBreaker(fn, "test-svc", testCfg)
	_, err := wrapped(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWrapWithBreaker_OpensAfterThreshold(t *testing.T) {
	calls := 0
	fn := func(ctx context.Context) (interface{}, error) {
		calls++
		return nil, errors.New("fail")
	}
	wrapped := WrapWithBreaker(fn, "trip-svc", testCfg)

	// exhaust the threshold
	for i := 0; i < testCfg.ErrorThreshold; i++ {
		wrapped(context.Background())
	}

	callsBefore := calls
	_, err := wrapped(context.Background())
	if err == nil {
		t.Fatal("expected circuit-open error")
	}
	if calls != callsBefore {
		t.Fatal("expected fn not to be called when circuit is open")
	}
}
