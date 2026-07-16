package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsSensitiveKeysAndValues(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := New(&output, "debug")
	logger.Info(
		"safe",
		"api_key",
		"do-not-print",
		"url",
		"post"+"gres://"+"user:password"+"@example.test/database",
		"nested",
		slog.GroupValue(
			slog.String("authorization", "Bearer private"),
			slog.String("safe", "visible"),
		),
	)
	text := output.String()
	for _, secret := range []string{"do-not-print", "password", "Bearer private"} {
		if strings.Contains(text, secret) {
			t.Fatalf("secret leaked in log: %s", text)
		}
	}
	if !strings.Contains(text, redacted) || !strings.Contains(text, "visible") {
		t.Fatalf("unexpected redacted log: %s", text)
	}
}

func TestLoggerHonorsLevel(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := New(&output, "warn")
	logger.Info("hidden")
	logger.Warn("visible")
	if strings.Contains(output.String(), "hidden") ||
		!strings.Contains(output.String(), "visible") {
		t.Fatalf("unexpected level output: %s", output.String())
	}
}

func TestLoggerRedactsMessagesAndErrors(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := New(&output, "debug")
	secret := "Bearer " + "secret-token-value"
	logger.Error(secret)
	logger.Error("operation failed", "error", errors.New(secret))
	if strings.Contains(output.String(), secret) {
		t.Fatalf("secret leaked through message or error: %s", output.String())
	}
	if strings.Count(output.String(), redacted) != 2 {
		t.Fatalf("expected two redactions: %s", output.String())
	}
}
