package logging

import (
	"context"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

func WithSpanContext(ctx context.Context, record *log.Record) {
	span := trace.SpanFromContext(ctx)
	if !span.IsRecording() {
		return
	}

	sc := span.SpanContext()
	if !sc.IsValid() {
		return
	}

	record.AddAttributes(
		log.String("trace_id", sc.TraceID().String()),
		log.String("span_id", sc.SpanID().String()),
	)
}