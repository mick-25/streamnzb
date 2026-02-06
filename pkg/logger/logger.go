package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

var Log *slog.Logger

// BroadcastHandler wraps a slog.Handler and sends records to a channel
type BroadcastHandler struct {
	slog.Handler
	ch chan<- string
}

func (h *BroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	// 1. Write to standard output (original handler)
	err := h.Handler.Handle(ctx, r)

	// 2. Broadcast to channel (non-blocking)
	if h.ch != nil {
		// Simple text formatting for the UI
		msg := fmt.Sprintf("time=%s level=%s msg=%q", r.Time.Format(time.RFC3339), r.Level, r.Message)
		r.Attrs(func(a slog.Attr) bool {
			msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
			return true
		})
		
		select {
		case h.ch <- msg:
		default:
			// Drop if channel is full to avoid blocking
		}
	}
	return err
}

var broadcastCh chan<- string

// SetBroadcast sets a channel to receive log messages
func SetBroadcast(ch chan<- string) {
	broadcastCh = ch
	// Re-init listener is hard without storing current level, 
	// so we rely on the custom handler checking the global var or 
	// valid re-init.
	// Actually, easier: modify Init to wrap the handler if broadcastCh is set.
	// But Init creates a NEW logger. 
}

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

	baseHandler := slog.NewTextHandler(os.Stdout, opts)
	
	// Always wrap with our broadcaster
	// We use the global broadcastCh so we can set it anytime
	handler := &GlobalBroadcastHandler{
		Handler: baseHandler,
	}
	
	Log = slog.New(handler)
	slog.SetDefault(Log)
}

// GlobalBroadcastHandler uses the package-level broadcastCh
type GlobalBroadcastHandler struct {
	slog.Handler
}

var (
	history   []string
	historyMu sync.RWMutex
	maxHistory = 500
)

func (h *GlobalBroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.Handler.Handle(ctx, r)
	
	// Format message
	msg := fmt.Sprintf("time=%s level=%s msg=%q", r.Time.Format("2006-01-02T15:04:05.000Z07:00"), r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})

	// 1. Store in history
	historyMu.Lock()
	if len(history) >= maxHistory {
		history = history[1:]
	}
	history = append(history, msg)
	historyMu.Unlock()

	// 2. Broadcast
	if broadcastCh != nil {
		select {
		case broadcastCh <- msg:
		default:
		}
	}
	return err
}

// GetHistory returns the current log history
func GetHistory() []string {
	historyMu.RLock()
	defer historyMu.RUnlock()
	// Return a copy
	cp := make([]string, len(history))
	copy(cp, history)
	return cp
}

// SetLevel updates the logger level at runtime
func SetLevel(levelStr string) {
	Init(levelStr)
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
