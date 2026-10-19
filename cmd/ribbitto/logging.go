package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

func configureLogging(format string) error {
	return configureLoggingTo(format, os.Stderr)
}

func configureLoggingTo(format string, output io.Writer) error {
	switch format {
	case "":
		return nil
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(output, nil)))
		return nil
	default:
		return fmt.Errorf("RIBBITTO_LOG_FORMAT must be json or empty")
	}
}
