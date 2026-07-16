// Package logging provides structured logs with automatic secret redaction.
package logging

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
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
	clean := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attribute slog.Attr) bool {
		clean.AddAttrs(redactAttr(attribute))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *redactingHandler) WithAttrs(attributes []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, 0, len(attributes))
	for _, attribute := range attributes {
		clean = append(clean, redactAttr(attribute))
	}
	return &redactingHandler{inner: h.inner.WithAttrs(clean)}
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
		if sensitiveValue.MatchString(value) || strings.Contains(value, "PRIVATE KEY") {
			return slog.String(attribute.Key, redacted)
		}
	}
	return attribute
}

func attrsToAny(attributes []slog.Attr) []any {
	values := make([]any, len(attributes))
	for index := range attributes {
		values[index] = attributes[index]
	}
	return values
}
