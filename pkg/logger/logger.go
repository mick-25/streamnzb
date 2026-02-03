package logger

import (
	"log/slog"
	"os"
	"strings"
)

var Log *slog.Logger

// Init initializes the global logger
func Init(levelStr string) {
	var level slog.Level
	switch strings.ToUpper(levelStr) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	Log = slog.New(handler)
	slog.SetDefault(Log)
}

// Helper functions for easy access
func Debug(msg string, args ...any) {
	Log.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	Log.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	Log.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	Log.Error(msg, args...)
}

func Fatal(msg string, args ...any) {
	Log.Error(msg, args...)
	os.Exit(1)
}
