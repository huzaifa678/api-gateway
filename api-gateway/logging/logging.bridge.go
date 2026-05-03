package logging

import (
	"context"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
)

type OTelSlogLogger struct {
	slog   *slog.Logger
	otel   log.Logger
}

func NewOTelSlogLogger(serviceName string) *OTelSlogLogger {
	sl := slog.New(slog.NewTextHandler(os.Stdout, nil)).With("service", serviceName)
	return &OTelSlogLogger{
		slog: sl,
		otel: global.GetLoggerProvider().Logger(serviceName),
	}
}

// Log implements a kitlog-compatible variadic key-value logger so existing
// call sites (endpoint middleware, service, main) keep working unchanged.
func (l *OTelSlogLogger) Log(keyvals ...interface{}) error {
	ctx := context.Background()

	// Build slog attrs and OTel record in one pass.
	record := log.Record{}
	record.SetTimestamp(time.Now())
	record.SetObservedTimestamp(time.Now())
	record.SetSeverity(log.SeverityInfo)

	args := make([]any, 0, len(keyvals))
	for i := 0; i+1 < len(keyvals); i += 2 {
		key := interfaceToString(keyvals[i])
		val := keyvals[i+1]
		str := interfaceToString(val)

		switch key {
		case "msg":
			record.SetBody(log.StringValue(str))
		case "level":
			switch str {
			case "error":
				record.SetSeverity(log.SeverityError)
			case "warn":
				record.SetSeverity(log.SeverityWarn)
			default:
				record.SetSeverity(log.SeverityInfo)
			}
		default:
			record.AddAttributes(log.String(key, str))
		}
		args = append(args, slog.Any(key, val))
	}

	l.slog.LogAttrs(ctx, slog.LevelInfo, "", slog.Group("", args...))

	WithSpanContext(ctx, &record)
	l.otel.Emit(ctx, record)
	return nil
}

func interfaceToString(v interface{}) string {
	switch v := v.(type) {
	case string:
		return v
	case error:
		return v.Error()
	default:
		return slog.AnyValue(v).String()
	}
}
