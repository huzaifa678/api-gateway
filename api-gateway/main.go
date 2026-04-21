package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-kit/kit/log/level"
	kitlog "github.com/go-kit/log"
	_ "github.com/huzaifa678/SAAS-services/docs"
	"github.com/huzaifa678/SAAS-services/endpoint"
	"github.com/huzaifa678/SAAS-services/interceptor"
	"github.com/huzaifa678/SAAS-services/logging"
	"github.com/huzaifa678/SAAS-services/service"
	"github.com/huzaifa678/SAAS-services/tracing"
	"github.com/huzaifa678/SAAS-services/transport"
	"github.com/huzaifa678/SAAS-services/utils"
	"github.com/redis/go-redis/v9"
	httpSwagger "github.com/swaggo/http-swagger"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
)

var interruptSignals = []os.Signal{
	os.Interrupt,
	syscall.SIGTERM,
	syscall.SIGHUP,
	syscall.SIGQUIT,
}

// @title SAAS API Gateway
// @version 1.0
// @description API Gateway for Auth, Subscription and Billing Services
// @host localhost:9000
// @BasePath /
func main() {
	cfg := utils.Load()

	ctx := context.Background()
	ctx, span := otel.Tracer("api-gateway").Start(ctx, "request")
	defer span.End()

	shutdownLogger := logging.InitLogger(ctx, cfg.App.Name)
	defer shutdownLogger(context.Background())	
	
	logger := logging.NewOTelKitLogger(cfg.App.Name) 

	shutdownTracer := tracing.InitTracer(cfg.App.Name)
	defer shutdownTracer(context.Background())

	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals...)
	defer stop()
	
	waitGroup, ctx := errgroup.WithContext(ctx)

	runGoKitHTTP(ctx, waitGroup, cfg, logger)

	if err := waitGroup.Wait(); err != nil {
		level.Error(logger).Log("msg", "error during shutdown", "err", err)
	}
}

func runGoKitHTTP(ctx context.Context, waitGroup *errgroup.Group, cfg *utils.Config, logger kitlog.Logger) {

	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.URL,
	})

	keycloakJWKSURL := cfg.Keycloak.JWKSURL 

	subSvc := service.NewForwardService(
		cfg.Services.Subscription.URL,
		"subscription-service",
		"Subscription service temporarily unavailable",
		cfg.CircuitBreaker,
		logger,
	)

	authSvc := service.NewForwardService(
		cfg.Services.Auth.URL,
		"auth-service",
		"Auth service temporarily unavailable",
		cfg.CircuitBreaker,
		logger,
	)

	billSvc := service.NewForwardService(
		cfg.Services.Billing.URL,
		"billing-service",
		"Billing service temporarily unavailable",
		cfg.CircuitBreaker,
		logger,
	)

	authEndpoint := endpoint.MakeAuthEndpoint(authSvc)
	subEndpoint := endpoint.MakeSubscriptionEndpoint(subSvc)
	billEndpoint := endpoint.MakeBillingEndpoint(billSvc)

	authEndpoint = endpoint.LoggingMiddleware(logger)(authEndpoint)
	subEndpoint = endpoint.LoggingMiddleware(logger)(subEndpoint)
	billEndpoint = endpoint.LoggingMiddleware(logger)(billEndpoint)

	authEndpoint = endpoint.RateLimitMiddleware(redisClient, 10, 5, "auth", logger, 30*time.Second, )(authEndpoint)
	subEndpoint = endpoint.RateLimitMiddleware(redisClient, 5, 3, "sub", logger, 30*time.Second, )(subEndpoint)
	billEndpoint = endpoint.RateLimitMiddleware(redisClient, 5, 3, "bill", logger, 30*time.Second, )(billEndpoint)

	jwtMiddleware, err := interceptor.KeycloakMiddleware(keycloakJWKSURL)
	if err != nil {
		level.Error(logger).Log("msg", "failed to initialize Keycloak middleware", "err", err)
		return
	}

	subEndpoint = jwtMiddleware(subEndpoint)
	billEndpoint = jwtMiddleware(billEndpoint)

	authEndpoint = endpoint.TracedEndpoint("AuthEndpoint", authEndpoint)
	subEndpoint = endpoint.TracedEndpoint("SubscriptionEndpoint", subEndpoint)
	billEndpoint = endpoint.TracedEndpoint("BillingEndpoint", billEndpoint)

	authHandler := transport.NewGraphQLHTTPHandler(authEndpoint)
	subHandler := transport.NewGraphQLHTTPHandler(subEndpoint)
	billHandler := transport.NewRESTHTTPHandler(billEndpoint, logger)

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", authHandler)
	mux.Handle("/api/subscription/", subHandler)
	mux.Handle("/api/billing/", billHandler)
	mux.Handle("/swagger/", httpSwagger.Handler(
		httpSwagger.URL("http://localhost:9000/swagger/doc.json"),
	))

	corsHandler := transport.CORSMiddleware(cfg.CORS.AllowedOrigins)(mux)

	server := &http.Server{
		Addr:    ":" + cfg.App.Port,
		Handler: corsHandler,
	}

	level.Info(logger).Log(
		"msg", "API Gateway started",
		"port", cfg.App.Port,
	)

	waitGroup.Go(func() error {
		go func() {
			<-ctx.Done()
			level.Info(logger).Log("msg", "Shutting down API Gateway")

			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				level.Error(logger).Log("msg", "server shutdown failed", "err", err)
			} else {
				level.Info(logger).Log("msg", "API Gateway stopped gracefully")
			}
		}()

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	})
}