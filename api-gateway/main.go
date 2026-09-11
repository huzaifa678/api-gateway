package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/huzaifa678/SAAS-services/docs"
	"github.com/huzaifa678/SAAS-services/interceptor"
	"github.com/huzaifa678/SAAS-services/logging"
	"github.com/huzaifa678/SAAS-services/middleware"
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

// chain wraps h in the given middleware. The first middleware is the outermost
// (runs first on the way in), matching how the stack is read top to bottom.
func chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
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
	defer func() { _ = shutdownLogger(context.Background()) }()

	otelLogger := logging.NewOTelSlogLogger(cfg.App.Name)
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil)).With("service", cfg.App.Name)
	_ = otelLogger // OTel logger initialized for side-effects (global provider set)

	shutdownTracer := tracing.InitTracer(cfg.App.Name)
	defer func() { _ = shutdownTracer(context.Background()) }()

	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals...)
	defer stop()

	waitGroup, ctx := errgroup.WithContext(ctx)

	runHTTP(ctx, waitGroup, cfg, logger)

	if err := waitGroup.Wait(); err != nil {
		logger.ErrorContext(ctx, "error during shutdown", "err", err)
	}
}

func runHTTP(ctx context.Context, waitGroup *errgroup.Group, cfg *utils.Config, logger *slog.Logger) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.URL,
	})

	keycloakJWKSURL := cfg.Keycloak.JWKSURL

	subSvc := service.NewForwardService(cfg.Services.Subscription.URL, "subscription-service", "Subscription service temporarily unavailable", cfg.CircuitBreaker, logger)
	authSvc := service.NewForwardService(cfg.Services.Auth.URL, "auth-service", "Auth service temporarily unavailable", cfg.CircuitBreaker, logger)
	billSvc := service.NewForwardService(cfg.Services.Billing.URL, "billing-service", "Billing service temporarily unavailable", cfg.CircuitBreaker, logger)

	// Cache proxy sits outermost: a GET hit skips the breaker and upstream
	cacheTTL := time.Duration(cfg.Cache.TTLSeconds) * time.Second
	subSvc = service.NewCachingProxy(subSvc, redisClient, cacheTTL)
	authSvc = service.NewCachingProxy(authSvc, redisClient, cacheTTL)
	billSvc = service.NewCachingProxy(billSvc, redisClient, cacheTTL)

	authMW, err := interceptor.KeycloakMiddleware(keycloakJWKSURL)
	if err != nil {
		logger.ErrorContext(ctx, "failed to initialize Keycloak middleware", "err", err)
		return
	}

	authHandler := chain(transport.NewHandler(authSvc),
		middleware.Tracing("AuthEndpoint"),
		middleware.RateLimit(redisClient, 10, 5, "auth", logger, 30*time.Second),
		middleware.Logging(logger),
	)
	subHandler := chain(transport.NewHandler(subSvc),
		middleware.Tracing("SubscriptionEndpoint"),
		authMW,
		middleware.RateLimit(redisClient, 5, 3, "sub", logger, 30*time.Second),
		middleware.Logging(logger),
	)
	billHandler := chain(transport.NewHandler(billSvc),
		middleware.Tracing("BillingEndpoint"),
		authMW,
		middleware.RateLimit(redisClient, 5, 3, "bill", logger, 30*time.Second),
		middleware.Logging(logger),
	)

	mux := http.NewServeMux()
	mux.Handle("/api/auth/", authHandler)
	mux.Handle("/api/subscription/", subHandler)
	mux.Handle("/api/billing/", billHandler)
	// Swagger UI / OpenAPI is exposed only in dev — never in staging/prod, where
	// publishing the API surface widens the attack surface. Env comes from
	// GATEWAY_APP_ENV (see app.yaml / the Helm chart); anything but "dev" hides it.
	if strings.EqualFold(cfg.App.Env, "dev") {
		mux.Handle("/swagger/", httpSwagger.Handler(httpSwagger.URL(cfg.Swagger.URL)))
	}
	mux.HandleFunc("/healthz/live", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/healthz/ready", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	corsHandler := transport.CORSMiddleware(cfg.CORS.AllowedOrigins)(mux)

	server := &http.Server{
		Addr:    ":" + cfg.App.Port,
		Handler: corsHandler,
	}

	logger.InfoContext(ctx, "API Gateway started", "port", cfg.App.Port)

	waitGroup.Go(func() error {
		go func() {
			<-ctx.Done()
			logger.InfoContext(ctx, "Shutting down API Gateway")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.ErrorContext(ctx, "server shutdown failed", "err", err)
			} else {
				logger.InfoContext(ctx, "API Gateway stopped gracefully")
			}
		}()

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	})
}
