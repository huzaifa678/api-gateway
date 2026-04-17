package logging

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

func InitLogger(ctx context.Context, serviceName string) func(context.Context) error {
	lokiExporter, err := otlploghttp.New(ctx, 
		otlploghttp.WithEndpoint("localhost:43180"), 
		otlploghttp.WithInsecure(),
	)

	if err != nil {
        return func(context.Context) error {
            return fmt.Errorf("failed to create OTLP log exporter: %w", err)
        }
    }


	res, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(serviceName),
		),
	)

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(lokiExporter)), 
		sdklog.WithResource(res),
	)

	global.SetLoggerProvider(provider)
	return provider.Shutdown
}
