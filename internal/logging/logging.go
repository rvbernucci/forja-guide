// Package logging provides structured logs with automatic secret redaction.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

const redacted = "[REDACTED]"

var (
	sensitiveKey = regexp.MustCompile(
		`(?i)(authorization|api[_-]?key|password|passwd|secret|token|credential|private[_-]?key)`,
	)
	sensitiveValue = regexp.MustCompile(
		`(?i)(bearer\s+\S+|gh[opsu]_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{20,}|[a-z][a-z0-9+.-]*://[^/\s:@]+:[^@\s]+@)`,
	)
)

// New creates a JSON logger with the requested level and redaction.
func New(output io.Writer, level string) *slog.Logger {
	var configured slog.Level
	switch level {
	case "debug":
		configured = slog.LevelDebug
	case "warn":
		configured = slog.LevelWarn
	case "error":
		configured = slog.LevelError
	default:
		configured = slog.LevelInfo
	}
	base := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: configured})
	return slog.New(&redactingHandler{inner: base})
}

type redactingHandler struct {
	inner slog.Handler
}

func (h *redactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactingHandler) Handle(ctx context.Context, record slog.Record) error {
	message := record.Message
	if mustRedact(message) {
		message = redacted
	}
	clean := slog.NewRecord(record.Time, record.Level, message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		if reservedTraceKey(attribute.Key) {
			return true
		}
		clean.AddAttrs(redactAttr(attribute))
		return true
	})
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		clean.AddAttrs(
			slog.String("trace_id", spanContext.TraceID().String()),
			slog.String("span_id", spanContext.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		if reservedTraceKey(attribute.Key) {
			continue
		}
		clean = append(clean, redactAttr(attribute))
	}
	return &redactingHandler{inner: h.inner.WithAttrs(clean)}
}

func reservedTraceKey(key string) bool {
	return key == "trace_id" || key == "span_id"
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name)}
}

func redactAttr(attribute slog.Attr) slog.Attr {
	attribute.Value = attribute.Value.Resolve()
	if sensitiveKey.MatchString(attribute.Key) {
		return slog.String(attribute.Key, redacted)
	}
	if attribute.Value.Kind() == slog.KindGroup {
		group := attribute.Value.Group()
		clean := make([]slog.Attr, 0, len(group))
		for _, child := range group {
			clean = append(clean, redactAttr(child))
		}
		return slog.Group(attribute.Key, attrsToAny(clean)...)
	}
	if attribute.Value.Kind() == slog.KindString {
		value := attribute.Value.String()
		if mustRedact(value) {
			return slog.String(attribute.Key, redacted)
		}
	}
	if attribute.Value.Kind() == slog.KindAny {
		switch value := attribute.Value.Any().(type) {
		case error:
			rendered, ok := safeRender(value, value.Error)
			if !ok || mustRedact(rendered) {
				return slog.String(attribute.Key, redacted)
			}
			return slog.String(attribute.Key, rendered)
		case fmt.Stringer:
			rendered, ok := safeRender(value, value.String)
			if !ok || mustRedact(rendered) {
				return slog.String(attribute.Key, redacted)
			}
			return slog.String(attribute.Key, rendered)
		}
	}
	return attribute
}

func mustRedact(value string) bool {
	return sensitiveValue.MatchString(value) || strings.Contains(value, "PRIVATE KEY")
}

func safeRender(value any, render func() string) (text string, ok bool) {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		if reflected.IsNil() {
			return "<nil>", true
		}
	}
	defer func() {
		if recover() != nil {
			text = ""
			ok = false
		}
	}()
	return render(), true
}

func attrsToAny(attributes []slog.Attr) []any {
	values := make([]any, len(attributes))
	for index := range attributes {
		values[index] = attributes[index]
	}
	return values
}
