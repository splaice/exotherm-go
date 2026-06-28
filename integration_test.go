package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/exotherm/exotherm-go/internal/archive"
	"github.com/exotherm/exotherm-go/internal/catalyst"
	"github.com/exotherm/exotherm-go/internal/export"
)

// demoFixture is decrypted with the built-in defaultPassphrase (see the README).
const demoFixture = "testdata/demo/SupportLogs.tar.gz.gpg.aa"

// TestDemoFixture runs the full pipeline against the bundled demo archive:
// decrypt + untar, list sessions, and export a session to CSV.
func TestDemoFixture(t *testing.T) {
	dir := t.TempDir()

	parts, err := archive.CollectParts(demoFixture)
	if err != nil {
		t.Fatalf("collect parts: %v", err)
	}
	res, err := archive.Extract(parts, defaultPassphrase, dir)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Files == 0 {
		t.Fatal("extract produced no files")
	}

	b, err := catalyst.Open(dir)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer b.Close()

	sessions, err := b.ListSessions()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}

	byID := make(map[int64]catalyst.Session, len(sessions))
	for _, s := range sessions {
		byID[s.ID] = s
	}
	if _, ok := byID[124]; !ok {
		t.Error("missing session 124")
	}
	if _, ok := byID[64]; !ok {
		t.Error("missing session 64")
	}
	if got := byID[124].TrackName.String; got != "NCCAR" {
		t.Errorf("session 124 track = %q, want NCCAR", got)
	}

	laps, err := b.Laps("124")
	if err != nil {
		t.Fatalf("laps: %v", err)
	}
	if len(laps) == 0 {
		t.Fatal("session 124 has no laps")
	}

	var buf bytes.Buffer
	stats, err := export.SessionCSV(&buf, b, laps, export.MetersPerSecond)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if stats.Rows == 0 {
		t.Fatal("export produced no rows")
	}

	out := buf.String()
	if header, _, _ := strings.Cut(out, "\n"); !strings.HasPrefix(header, "Lap,Time,Lap Time,") {
		t.Errorf("unexpected CSV header: %q", header)
	}
	// header line + one line per data row
	if gotLines := strings.Count(out, "\n"); gotLines != stats.Rows+1 {
		t.Errorf("CSV lines = %d, want rows+header = %d", gotLines, stats.Rows+1)
	}
}
