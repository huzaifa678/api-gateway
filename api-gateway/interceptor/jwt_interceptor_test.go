package interceptor

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-secret"

func makeToken(secret string, userID string, expired bool) string {
	claims := MyClaims{
		UserID:           userID,
		RegisteredClaims: jwt.RegisteredClaims{},
	}
	if expired {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	return token
}

// okHandler records that the request reached the protected handler.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func runJWT(t *testing.T, authHeader string) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	reached := false
	h := JWTMiddleware(testSecret)(okHandler(&reached))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, reached
}

func TestJWTMiddleware_MissingHeader(t *testing.T) {
	rr, reached := runJWT(t, "")
	if reached {
		t.Fatal("handler should not be reached without a token")
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 for missing header, got %d", rr.Code)
	}
}

func TestJWTMiddleware_InvalidToken(t *testing.T) {
	rr, reached := runJWT(t, "Bearer bad.token.here")
	if reached {
		t.Fatal("handler should not be reached with an invalid token")
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 for invalid token, got %d", rr.Code)
	}
}

func TestJWTMiddleware_WrongSecret(t *testing.T) {
	token := makeToken("other-secret", "user1", false)
	rr, reached := runJWT(t, "Bearer "+token)
	if reached {
		t.Fatal("handler should not be reached with a wrong-secret token")
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 for wrong secret, got %d", rr.Code)
	}
}

func TestJWTMiddleware_ExpiredToken(t *testing.T) {
	token := makeToken(testSecret, "user1", true)
	rr, reached := runJWT(t, "Bearer "+token)
	if reached {
		t.Fatal("handler should not be reached with an expired token")
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("expected non-200 for expired token, got %d", rr.Code)
	}
}

func TestJWTMiddleware_ValidToken(t *testing.T) {
	token := makeToken(testSecret, "user42", false)

	var gotUserID any
	h := JWTMiddleware(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID = r.Context().Value(UserIDKey)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid token, got %d", rr.Code)
	}
	if gotUserID != "user42" {
		t.Fatalf("expected userId=user42 in context, got %v", gotUserID)
	}
}
