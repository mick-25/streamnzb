package paths

import (
	"os"
)

// GetDataDir returns the data directory path
// If running in Docker (/.dockerenv exists), returns /app/data
// Otherwise returns current directory (.)
func GetDataDir() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		// Running in Docker container
		return "/app/data"
	}
	return "."
}
