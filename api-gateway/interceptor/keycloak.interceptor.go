package interceptor

import (
	"context"
	"errors"
	"strings"

	"github.com/MicahParks/keyfunc/v2"
	kitendpoint "github.com/go-kit/kit/endpoint"
	"github.com/golang-jwt/jwt/v5"
	endpoint "github.com/huzaifa678/SAAS-services/endpoint"
)

type contextKey string

const UserClaimsKey contextKey = "user"

type KeycloakClaims struct {
	PreferredUsername string `json:"preferred_username"`
	Email             string `json:"email"`
	jwt.RegisteredClaims
}

func KeycloakMiddleware(jwksURL string) (kitendpoint.Middleware, error) {
	jwks, err := keyfunc.Get(jwksURL, keyfunc.Options{})
	if err != nil {
		return nil, err
	}

	return func(next kitendpoint.Endpoint) kitendpoint.Endpoint {
		return func(ctx context.Context, request interface{}) (interface{}, error) {
			// Expecting ForwardRequest from your endpoints
			req, ok := request.(endpoint.ForwardRequest)
			if !ok {
				return nil, errors.New("invalid request type, expected ForwardRequest")
			}

			authHeader := ""

			if vals, ok := req.Header["Authorization"]; ok && len(vals) > 0 {
				authHeader = vals[0]
			}

			if !strings.HasPrefix(authHeader, "Bearer ") {
				return nil, errors.New("unauthorized")
			}

			tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
			claims := &KeycloakClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, jwks.Keyfunc)
			if err != nil || !token.Valid {
				return nil, errors.New("unauthorized")
			}

			ctx = context.WithValue(ctx, UserClaimsKey, claims)
			return next(ctx, request)
		}
	}, nil
}
