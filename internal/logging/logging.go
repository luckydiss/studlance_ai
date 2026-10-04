// Package logging configures the structured logger.
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Setup installs a JSON slog logger writing to stdout and to
// <dataDir>/logs/server.log. It returns the open log file so the caller can
// close it, or nil if the file could not be opened (logging to stdout still works).
func Setup(dataDir string) *os.File {
	writers := []io.Writer{os.Stdout}

	var f *os.File
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err == nil {
		if opened, err := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f = opened
			writers = append(writers, f)
		}
	}

	handler := slog.NewJSONHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	return f
}
