package errors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

var (
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
	ErrStoragePressure   = errors.New("service busy (storage pressure)")
)

func EncodeError(_ context.Context, err error, w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")

	if errors.Is(err, ErrRateLimitExceeded) || errors.Is(err, ErrStoragePressure) {
		w.WriteHeader(http.StatusServiceUnavailable) // 503
	} else {
		w.WriteHeader(http.StatusInternalServerError) // 500
	}

	if err := json.NewEncoder(w).Encode(map[string]string{
		"error": err.Error(),
	}); err != nil {
		// Log encoding error but don't fail the response
		_ = err
	}
}
