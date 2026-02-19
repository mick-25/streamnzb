package nzb

import (
	"os"
	"path/filepath"
	"testing"

	"streamnzb/pkg/core/logger"
)

func TestCompressionType_posterAttribute(t *testing.T) {
	// Logger must be initialized; Debug calls happen inside CompressionType
	logger.Init("warn")
	// NZBs from NZBgeek and some indexers put the Usenet subject line in "poster"
	// instead of "subject". Verify we detect RAR correctly in that case.
	path := filepath.Join("..", "..", "..", "The.Hobbit.The.Desolation.Of.Smaug.Extended.(2013).HDR.10bit.2160p.BT2020.DTS.HD.MA-VISIONPLUSHDR1000.NLsubs.nzb")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("NZB file not found: %v", err)
	}
	nzb, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	ct := nzb.CompressionType()
	if ct != "rar" {
		t.Errorf("CompressionType() = %q, want %q", ct, "rar")
	}
	contentFiles := nzb.GetContentFiles()
	if len(contentFiles) == 0 {
		t.Error("GetContentFiles() returned empty, expected RAR parts")
	}
}
