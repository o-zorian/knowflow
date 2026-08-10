package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

func New(level, component string) (*slog.Logger, error) {
	return NewWithWriter(level, component, os.Stdout)
}

func NewWithWriter(level, component string, writer io.Writer) (*slog.Logger, error) {
	var slogLevel slog.Level
	switch level {
	case "debug":
		slogLevel = slog.LevelDebug
	case "info":
		slogLevel = slog.LevelInfo
	case "warn":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		return nil, fmt.Errorf("unsupported log level %q", level)
	}
	handler := slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: slogLevel})
	return slog.New(handler).With("component", component), nil
}
