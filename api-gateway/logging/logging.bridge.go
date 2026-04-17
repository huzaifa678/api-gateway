package logging

import (
	"context"
	"fmt"
	"os"
	"time"

	kitlog "github.com/go-kit/log"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
)

type OTelKitLogger struct {
	logger log.Logger
	kitLogger kitlog.Logger
}

func NewOTelKitLogger(serviceName string) *OTelKitLogger {

	kitLogger := kitlog.NewLogfmtLogger(os.Stdout)
	kitLogger = kitlog.With(
		kitLogger,
		"ts", kitlog.DefaultTimestampUTC,
		"service", serviceName,
	)
	
	return &OTelKitLogger{
		logger: global.GetLoggerProvider().Logger(serviceName), // using global logger from open-telementry
		kitLogger: kitLogger,
	}
}

func (l *OTelKitLogger) Log(keyvals ...interface{}) error {

	ctx := context.Background() 

	_ = l.kitLogger.Log(keyvals...)

	record := log.Record{}
	record.SetTimestamp(time.Now())
	record.SetObservedTimestamp(time.Now())

	var severitySet bool

	for i := 0; i < len(keyvals); i += 2 {
		if i+1 >= len(keyvals) {
			break
		}

		key := fmt.Sprintf("%v", keyvals[i])
		val := keyvals[i+1] 

		switch key {

		case "msg":
			record.SetBody(log.StringValue(interfaceToString(val)))

		case "level":
			severitySet = true
			switch interfaceToString(val) {
			case "info":
				record.SetSeverity(log.SeverityInfo)
			case "error":
				record.SetSeverity(log.SeverityError)
			case "warn":
				record.SetSeverity(log.SeverityWarn)
			default:
				record.SetSeverity(log.SeverityInfo)
			}

		default:
			record.AddAttributes(log.String(key, interfaceToString(val)))
		}
	}

	if !severitySet {
		record.SetSeverity(log.SeverityInfo)
	}

	WithSpanContext(ctx, &record)

	l.logger.Emit(ctx, record)
	return nil
}

func interfaceToString(v interface{}) string {
	switch v := v.(type) {
	case string:
		return v
	case error:
		return v.Error()
	default:
		return "" + fmt.Sprintf("%v", v)
	}
}
