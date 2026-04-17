package interceptor

import (
	"context"
	"errors"
	"strings"

	kitendpoint "github.com/go-kit/kit/endpoint"
	"github.com/golang-jwt/jwt/v5"
	"github.com/huzaifa678/SAAS-services/endpoint"
)

type MyClaims struct {
	UserID string `json:"userId"`
	jwt.RegisteredClaims
}


func JWTMiddleware(secret string) kitendpoint.Middleware {
    return func(next kitendpoint.Endpoint) kitendpoint.Endpoint {
        return func(ctx context.Context, request interface{}) (interface{}, error) {
            req := request.(endpoint.ForwardRequest) 
            authHeader := ""
            if vals, ok := req.Header["Authorization"]; ok && len(vals) > 0 {
                authHeader = vals[0]
            }

            if !strings.HasPrefix(authHeader, "Bearer ") {
                return nil, errors.New("unauthorized")
            }

            tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

            claims := &MyClaims{}
            token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
                return []byte(secret), nil
            })
            if err != nil || !token.Valid {
                return nil, errors.New("unauthorized")
            }

            ctx = context.WithValue(ctx, "userId", claims.UserID)
            return next(ctx, request)
        }
    }
}
