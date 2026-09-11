package interceptor

import (
	"context"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/huzaifa678/SAAS-services/errors"
)

const UserIDKey contextKey = "userId"

type MyClaims struct {
	UserID string `json:"userId"`
	jwt.RegisteredClaims
}

// standard net/http middleware
func JWTMiddleware(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				errors.EncodeError(r.Context(), errUnauthorized, w)
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims := &MyClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
				return []byte(secret), nil
			})
			if err != nil || !token.Valid {
				errors.EncodeError(r.Context(), errUnauthorized, w)
				return
			}

			ctx := context.WithValue(r.Context(), UserIDKey, claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
