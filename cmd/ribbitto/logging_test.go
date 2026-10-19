package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestConfigureLogging(t *testing.T) {
	for _, format := range []string{"", "json", "xml", "JSON", " json", "text"} {
		t.Run(format, func(t *testing.T) {
			previous := slog.Default()
			t.Cleanup(func() { slog.SetDefault(previous) })
			var logs bytes.Buffer
			err := configureLoggingTo(format, &logs)
			switch format {
			case "json":
				if err != nil {
					t.Fatal(err)
				}
				slog.Info("format probe")
				var record map[string]any
				if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
					t.Fatal(err)
				}
				if record["msg"] != "format probe" || record["level"] != "INFO" {
					t.Fatal(record)
				}
			case "":
				if err != nil {
					t.Fatal(err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), "RIBBITTO_LOG_FORMAT") || !strings.Contains(err.Error(), "json or empty") {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if format != "json" && (slog.Default() != previous || slog.Default().Handler() != previous.Handler() || logs.Len() != 0) {
				t.Fatal("changed the default logger or wrote output")
			}
		})
	}
}

func TestRunRejectsLogFormatFirst(t *testing.T) {
	previous, args := slog.Default(), os.Args
	t.Cleanup(func() { slog.SetDefault(previous); os.Args = args })
	os.Args = []string{"ribbitto"}
	t.Setenv("RIBBITTO_LOG_FORMAT", "xml")
	t.Setenv("RIBBITTO_DATABASE_URL", "")
	if err := run(); err == nil || !strings.Contains(err.Error(), "RIBBITTO_LOG_FORMAT") {
		t.Fatalf("want log format error before database configuration, got %v", err)
	}
}
