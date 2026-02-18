package persistence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"streamnzb/pkg/core/logger"
	"testing"
)

func TestStateManager(t *testing.T) {
	logger.Init("DEBUG")
	tempDir, err := os.MkdirTemp("", "state_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}

	// Test Set and Get
	key := "test_key"
	value := map[string]string{"foo": "bar"}
	if err := mgr.Set(key, value); err != nil {
		t.Fatalf("failed to set value: %v", err)
	}

	var retrieved map[string]string
	found, err := mgr.Get(key, &retrieved)
	if err != nil {
		t.Fatalf("failed to get value: %v", err)
	}
	if !found {
		t.Fatal("value not found")
	}
	if retrieved["foo"] != "bar" {
		t.Errorf("expected bar, got %s", retrieved["foo"])
	}

	// Flush so debounced save runs before we reload (Set() schedules save after 2s)
	if err := mgr.Flush(); err != nil {
		t.Fatalf("failed to flush: %v", err)
	}

	// Test Persistence
	globalManager = nil // Reset global for reload
	mgr2, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to reload manager: %v", err)
	}

	var retrieved2 map[string]string
	found2, err := mgr2.Get(key, &retrieved2)
	if err != nil {
		t.Fatalf("failed to get value after reload: %v", err)
	}
	if !found2 {
		t.Fatal("value not found after reload")
	}
	if retrieved2["foo"] != "bar" {
		t.Errorf("expected bar after reload, got %s", retrieved2["foo"])
	}
}

func TestMigration(t *testing.T) {
	logger.Init("DEBUG")
	tempDir, err := os.MkdirTemp("", "migration_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create legacy usage.json
	usageData := map[string]interface{}{
		"indexer1": map[string]interface{}{
			"api_hits_used":  10,
			"last_reset_day": "2024-01-01",
		},
	}
	usagePath := filepath.Join(tempDir, "usage.json")
	data, _ := json.Marshal(usageData)
	os.WriteFile(usagePath, data, 0644)

	// Initialize manager
	globalManager = nil
	mgr, err := GetManager(tempDir)
	if err != nil {
		t.Fatalf("failed to get manager: %v", err)
	}

	// Verify migration
	var migratedUsage map[string]interface{}
	found, err := mgr.Get("indexer_usage", &migratedUsage)
	if err != nil || !found {
		t.Fatalf("migration failed: %v", err)
	}

	indexer1, ok := migratedUsage["indexer1"].(map[string]interface{})
	if !ok {
		t.Fatal("indexer1 not found in migrated data")
	}
	if indexer1["api_hits_used"].(float64) != 10 {
		t.Errorf("expected 10, got %v", indexer1["api_hits_used"])
	}

	// Verify usage.json is gone
	if _, err := os.Stat(usagePath); !os.IsNotExist(err) {
		t.Error("usage.json should have been deleted after migration")
	}
}
