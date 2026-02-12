package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"streamnzb/pkg/paths"
)

var Log *slog.Logger

// BroadcastHandler wraps a slog.Handler and sends records to a channel
// Note: This handler is currently unused but kept for potential future use
type BroadcastHandler struct {
	slog.Handler
	ch chan<- string
}

func (h *BroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	// 1. Write to standard output (original handler)
	err := h.Handler.Handle(ctx, r)

	// 2. Broadcast to channel (non-blocking)
	if h.ch != nil {
		// Use configured timezone (from TZ environment variable)
		locationMu.RLock()
		loc := logLocation
		locationMu.RUnlock()
		if loc == nil {
			loc = time.Local
		}
		// r.Time is in local timezone, convert to UTC first, then to configured timezone
		utcTime := r.Time.UTC()
		formattedTime := utcTime.In(loc)
		// Simple text formatting for the UI
		msg := fmt.Sprintf("time=%s level=%s msg=%q", formattedTime.Format("2006-01-02T15:04:05.000-07:00"), r.Level, r.Message)
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

// Prevent unused import warning by referencing time package
var _ = time.Now

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

	// Load timezone from TZ environment variable
	tzEnv := os.Getenv("TZ")
	var loc *time.Location
	locationMu.Lock()
	if tzEnv != "" {
		loadedLoc, err := time.LoadLocation(tzEnv)
		if err != nil {
			// If TZ is invalid, fall back to local timezone
			loc = time.Local
			logLocation = time.Local
		} else {
			loc = loadedLoc
			logLocation = loadedLoc
		}
	} else {
		// No TZ set, use local timezone
		loc = time.Local
		logLocation = time.Local
	}
	locationMu.Unlock()

	// Determine log file path using common data directory
	dataDir := paths.GetDataDir()
	// Create log filename with date: streamnzb-YYYY-MM-DD.log (one file per day)
	// Use configured timezone for date
	dateStr := time.Now().In(loc).Format("2006-01-02")
	logFileName := fmt.Sprintf("streamnzb-%s.log", dateStr)
	logFilePath := filepath.Join(dataDir, logFileName)

	// Ensure data directory exists and open log file
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log directory: %v\n", err)
	} else {
		// Open file in append mode
		logFileMu.Lock()
		if logFile != nil {
			logFile.Close()
		}
		var err error
		logFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v\n", logFilePath, err)
			logFile = nil
		}
		logFileMu.Unlock()
	}

	// Create handler options with ReplaceAttr to use configured timezone
	// Capture the location in the closure
	tzLoc := loc
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Replace time attribute with time in configured timezone
			if a.Key == slog.TimeKey {
				// a.Value.Time() returns UTC time, convert to configured timezone
				t := a.Value.Time().In(tzLoc)
				return slog.String("time", t.Format("2006-01-02T15:04:05.000-07:00"))
			}
			return a
		},
	}

	// Create handler that writes to stdout only (log file is written separately in GlobalBroadcastHandler)
	baseHandler := slog.NewTextHandler(os.Stdout, opts)

	// Always wrap with our broadcaster
	// We use the global broadcastCh so we can set it anytime
	handler := &GlobalBroadcastHandler{
		Handler: baseHandler,
	}

	Log = slog.New(handler)
	slog.SetDefault(Log)

	// Log timezone info for debugging (after logger is created)
	locationMu.RLock()
	currentLoc := logLocation
	currentTZEnv := tzEnv
	locationMu.RUnlock()
	if currentLoc != nil {
		Log.Info("Logger initialized", "timezone", currentLoc.String(), "tz_env", currentTZEnv)
	}
}

// GlobalBroadcastHandler uses the package-level broadcastCh
type GlobalBroadcastHandler struct {
	slog.Handler
}

var (
	history     []string
	historyMu   sync.RWMutex
	maxHistory  = 500
	logFile     *os.File
	logFileMu   sync.Mutex
	logLocation *time.Location
	locationMu  sync.RWMutex
)

func (h *GlobalBroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	// Use configured timezone (from TZ environment variable)
	locationMu.RLock()
	loc := logLocation
	locationMu.RUnlock()

	// Fallback to local timezone if not initialized (shouldn't happen, but be safe)
	if loc == nil {
		loc = time.Local
	}

	// r.Time from slog is in UTC, convert directly to configured timezone
	formattedTime := r.Time.In(loc)

	// Format message with configured timezone (show offset to verify conversion)
	msg := fmt.Sprintf("time=%s level=%s msg=%q", formattedTime.Format("2006-01-02T15:04:05.000-07:00"), r.Level, r.Message)
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

	// 2. Write to base handler (stdout only)
	err := h.Handler.Handle(ctx, r)

	// 3. Write to log file with custom format
	logFileMu.Lock()
	if logFile != nil {
		fmt.Fprintln(logFile, msg)
	}
	logFileMu.Unlock()

	// 4. Broadcast
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
	// Preserve log file when reinitializing
	logFileMu.Lock()
	currentLogFile := logFile
	logFileMu.Unlock()

	Init(levelStr)

	// Restore log file reference
	if currentLogFile != nil {
		logFileMu.Lock()
		logFile = currentLogFile
		logFileMu.Unlock()
	}
}

// Close closes the log file if one is open
func Close() {
	logFileMu.Lock()
	defer logFileMu.Unlock()
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
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
