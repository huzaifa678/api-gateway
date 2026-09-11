package interceptor

import (
	"context"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/huzaifa678/SAAS-services/errors"
)

type contextKey string

const UserClaimsKey contextKey = "user"

var errUnauthorized = stderrors.New("unauthorized")

type KeycloakClaims struct {
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	jwt.RegisteredClaims
}

// KeycloakMiddleware validates the request's bearer token against the Keycloak
// JWKS and stores the parsed claims in the request context. It is a standard
// net/http middleware; an unauthorized request is rejected before it reaches
// the downstream handler.
func KeycloakMiddleware(jwksURL string) (func(http.Handler) http.Handler, error) {
	jwks, err := keyfunc.Get(jwksURL, keyfunc.Options{})
	if err != nil {
		return nil, err
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				errors.EncodeError(r.Context(), errUnauthorized, w)
				return
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims := &KeycloakClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, jwks.Keyfunc)
			if err != nil || !token.Valid {
				errors.EncodeError(r.Context(), errUnauthorized, w)
				return
			}

			ctx := context.WithValue(r.Context(), UserClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}, nil
}
