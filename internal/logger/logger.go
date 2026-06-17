package logger

import (
	"log/slog"
	"os"
)

// Setup initializes the global slog logger.
// In a real application, you might want to add options for JSON format, log levels, etc.
func Setup(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	logger := slog.New(handler)
	slog.SetDefault(logger)
}
