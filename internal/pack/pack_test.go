package pack

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exotherm/exotherm-go/internal/catalyst"
	"github.com/exotherm/exotherm-go/internal/export"
)

func TestResolveDefaults(t *testing.T) {
	dest, tmp, speed, cleanup, err := (Config{Speed: "mph"}).Resolve("/Users/me")
	if err != nil {
		t.Fatal(err)
	}
	if dest != "/Users/me/exotherm" || tmp != "/Users/me/exotherm/tmp" {
		t.Fatalf("paths = %s %s", dest, tmp)
	}
	if speed != export.MilesPerHour || !cleanup {
		t.Fatalf("speed %s cleanup %v", speed, cleanup)
	}
}

func TestResolveCleanupOff(t *testing.T) {
	off := false
	_, tmp, _, cleanup, err := (Config{Dest: "/data/lib", Tmp: "/data/scratch", Speed: "mph", Cleanup: &off}).Resolve("/Users/me")
	if err != nil {
		t.Fatal(err)
	}
	if tmp != "/data/scratch" || cleanup {
		t.Fatalf("tmp %s cleanup %v", tmp, cleanup)
	}
}

func TestResolveRejectsSamePath(t *testing.T) {
	_, _, _, _, err := (Config{Dest: "/data", Tmp: "/data", Speed: "mph"}).Resolve("/Users/me")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDecide(t *testing.T) {
	dir := t.TempDir()
	if got := decide(nil, dir, videoMissing); got != actPack {
		t.Fatalf("new = %v", got)
	}
	csvPath := filepath.Join(dir, "session.csv")
	if err := os.WriteFile(csvPath, []byte("h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := &Entry{Video: "ok"}
	if got := decide(entry, dir, videoOK); got != actPack {
		t.Fatalf("ok entry without video file = %v, want pack", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "video.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := decide(entry, dir, videoOK); got != actSkip {
		t.Fatalf("complete = %v", got)
	}
	if err := os.Remove(filepath.Join(dir, "video.mp4")); err != nil {
		t.Fatal(err)
	}
	entry.Video = "missing"
	if got := decide(entry, dir, videoOK); got != actAddVideo {
		t.Fatalf("fill video = %v", got)
	}
	if got := decide(entry, dir, videoMissing); got != actSkip {
		t.Fatalf("still missing = %v", got)
	}
}

func TestMP4Moov(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.mp4")
	if err := os.WriteFile(good, append(box("ftyp", []byte("mp42")), box("moov", []byte("hdr"))...), 0o644); err != nil {
		t.Fatal(err)
	}
	if classifyVideo(good) != videoOK {
		t.Fatal("good mp4")
	}
	broken := filepath.Join(dir, "broken.mp4")
	// A box header that claims more bytes than the file contains.
	payload := box("ftyp", []byte("mp42"))
	payload = append(payload, 0, 0, 1, 0, 'm', 'd', 'a', 't')
	if err := os.WriteFile(broken, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if classifyVideo(broken) != videoBroken {
		t.Fatal("truncated mp4")
	}
	if classifyVideo(filepath.Join(dir, "nope.mp4")) != videoMissing {
		t.Fatal("missing")
	}
}

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func TestPackDemo(t *testing.T) {
	card := t.TempDir()
	garmin := filepath.Join(card, "Garmin")
	if err := os.Mkdir(garmin, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("..", "..", "testdata", "demo", "SupportLogs.tar.gz.gpg.aa")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(garmin, "SupportLogs.tar.gz.gpg.aa"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	tmp := filepath.Join(t.TempDir(), "scratch")

	var buf bytes.Buffer
	sum, err := Run(Options{
		Card:       card,
		Dest:       dest,
		Tmp:        tmp,
		Speed:      export.MilesPerHour,
		Cleanup:    true,
		Passphrase: "FF59E1881B4ED01C5C2F5F01890AFF25E9764D27",
		Out:        &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("tmp = %v, want removed", err)
	}
	log := buf.String()
	if !strings.Contains(log, "identified new pack, extracting") || !strings.Contains(log, "packed ") {
		t.Fatalf("log:\n%s", log)
	}
	if sum.Packed == 0 || sum.Skipped != 0 {
		t.Fatalf("summary %+v", sum)
	}
	idx, err := loadIndex(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Sessions) != sum.Packed {
		t.Fatalf("index %d summary packed %d", len(idx.Sessions), sum.Packed)
	}
	for _, e := range idx.Sessions {
		csvPath := filepath.Join(dest, e.Path, "session.csv")
		body, err := os.ReadFile(csvPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(body), "Lap,Time,Lap Time,") {
			t.Fatalf("csv header %q", body[:40])
		}
		if _, err := os.Stat(filepath.Join(dest, e.Path, "session.json")); err != nil {
			t.Fatal(err)
		}
	}

	buf.Reset()
	sum, err = Run(Options{
		Card:       card,
		Dest:       dest,
		Tmp:        tmp,
		Speed:      export.MilesPerHour,
		Cleanup:    false,
		Passphrase: "FF59E1881B4ED01C5C2F5F01890AFF25E9764D27",
		Out:        &buf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Skipped != len(idx.Sessions) || sum.Packed != 0 {
		t.Fatalf("second run %+v\n%s", sum, buf.String())
	}
	if !strings.Contains(buf.String(), "identified prior pack, skipping") {
		t.Fatalf("log:\n%s", buf.String())
	}
	if _, err := os.Stat(filepath.Join(tmp, "bundle")); err != nil {
		t.Fatalf("keep tmp: %v", err)
	}
}

func TestSessionDirName(t *testing.T) {
	s := catalyst.Session{
		ID:            43,
		StartTimeUTCs: 1770504393,
		TrackName:     sql.NullString{String: "Sonoma Raceway", Valid: true},
		Make:          sql.NullString{String: "ACURA", Valid: true},
		Model:         sql.NullString{String: "NSX", Valid: true},
	}
	got := sessionDirName(s, -28800)
	if got != "43-2026-02-07-1446-sonoma-raceway-acura-nsx" {
		t.Fatalf("dir %s", got)
	}
}
