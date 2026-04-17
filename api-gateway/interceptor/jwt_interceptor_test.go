package interceptor

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	kitendpoint "github.com/go-kit/kit/endpoint"
	"github.com/huzaifa678/SAAS-services/endpoint"
)

const testSecret = "test-secret"

func makeToken(secret string, userID string, expired bool) string {
	claims := MyClaims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{},
	}
	if expired {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	return token
}

func passthroughEndpoint(_ context.Context, req interface{}) (interface{}, error) {
	return req, nil
}

func TestJWTMiddleware_MissingHeader(t *testing.T) {
	mw := JWTMiddleware(testSecret)(kitendpoint.Endpoint(passthroughEndpoint))
	req := endpoint.ForwardRequest{Header: map[string][]string{}}
	_, err := mw(context.Background(), req)
	if err == nil || err.Error() != "unauthorized" {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestJWTMiddleware_InvalidToken(t *testing.T) {
	mw := JWTMiddleware(testSecret)(kitendpoint.Endpoint(passthroughEndpoint))
	req := endpoint.ForwardRequest{Header: map[string][]string{"Authorization": {"Bearer bad.token.here"}}}
	_, err := mw(context.Background(), req)
	if err == nil || err.Error() != "unauthorized" {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestJWTMiddleware_WrongSecret(t *testing.T) {
	token := makeToken("other-secret", "user1", false)
	mw := JWTMiddleware(testSecret)(kitendpoint.Endpoint(passthroughEndpoint))
	req := endpoint.ForwardRequest{Header: map[string][]string{"Authorization": {"Bearer " + token}}}
	_, err := mw(context.Background(), req)
	if err == nil || err.Error() != "unauthorized" {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestJWTMiddleware_ExpiredToken(t *testing.T) {
	token := makeToken(testSecret, "user1", true)
	mw := JWTMiddleware(testSecret)(kitendpoint.Endpoint(passthroughEndpoint))
	req := endpoint.ForwardRequest{Header: map[string][]string{"Authorization": {"Bearer " + token}}}
	_, err := mw(context.Background(), req)
	if err == nil || err.Error() != "unauthorized" {
		t.Fatalf("expected unauthorized, got %v", err)
	}
}

func TestJWTMiddleware_ValidToken(t *testing.T) {
	token := makeToken(testSecret, "user42", false)
	var capturedCtx context.Context
	next := func(ctx context.Context, req interface{}) (interface{}, error) {
		capturedCtx = ctx
		return req, nil
	}
	mw := JWTMiddleware(testSecret)(kitendpoint.Endpoint(next))
	req := endpoint.ForwardRequest{Header: map[string][]string{"Authorization": {"Bearer " + token}}}
	_, err := mw(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedCtx.Value("userId") != "user42" {
		t.Fatalf("expected userId=user42 in context, got %v", capturedCtx.Value("userId"))
	}
}
