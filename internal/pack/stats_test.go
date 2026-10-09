package pack

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStats(t *testing.T) {
	dest := t.TempDir()
	full := filepath.Join(dest, "sessions", "43-2026-02-07-1446-sonoma-raceway-acura-nsx")
	broken := filepath.Join(dest, "sessions", "36-2025-10-17-1252-sonoma")
	optimalOnly := filepath.Join(dest, "sessions", "8-2024-03-09-0832-sonoma")
	averageOnly := filepath.Join(dest, "sessions", "9-2024-03-09-1001-sonoma")
	unnamed := filepath.Join(dest, "sessions", "notes")
	for _, dir := range []string{full, broken, optimalOnly, averageOnly, unnamed} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(full, "session.csv"), "Lap,Time\n1,1\n0,2\n-1,3\n")
	write(filepath.Join(full, "session.json"), `{
		"session_id": 43,
		"when": "2026-02-07T14:46:33-08:00",
		"track": "Sonoma Raceway",
		"car": "ACURA NSX"
	}`)
	write(filepath.Join(full, "video.mp4"), string(append(box("ftyp", []byte("mp42")), box("moov", []byte("hdr"))...)))
	write(filepath.Join(full, "optimal.mp4"), string(append(box("ftyp", []byte("mp42")), box("moov", []byte("hdr"))...)))

	write(filepath.Join(broken, "session.csv"), "Lap,Time\n")
	payload := box("ftyp", []byte("mp42"))
	payload = append(payload, 0, 0, 1, 0, 'm', 'd', 'a', 't')
	write(filepath.Join(broken, "video.mp4"), string(payload))
	write(filepath.Join(broken, "session.json"), "{")

	write(filepath.Join(optimalOnly, "session.csv"), "Lap,Time\n1,1\n")
	write(filepath.Join(optimalOnly, "optimal.csv"), "SessionID,Lap\n42,1\n")
	write(filepath.Join(optimalOnly, "optimal.mp4"), string(append(box("ftyp", []byte("mp42")), box("moov", []byte("hdr"))...)))

	write(filepath.Join(averageOnly, "session.csv"), "Lap,Time\n-1,1\n")
	write(filepath.Join(averageOnly, "session.json"), `{"session_id": 9, "when": "2024-03-09T10:01:00-08:00", "track": "Sonoma Raceway", "car": "MAZDA MIATA"}`)

	if err := saveIndex(dest, Index{Sessions: []Entry{
		{GUID: "g36", SessionID: 36, When: "2025-10-17T12:52:08-07:00", Track: "Sonoma Raceway", Car: "ACURA NSX", Path: "sessions/36-2025-10-17-1252-sonoma", Video: "ok"},
		{GUID: "g99", SessionID: 99, When: "2024-01-02T03:04:00Z", Track: "Laguna", Car: "Miata", Path: "sessions/99-missing", Video: "ok"},
	}}); err != nil {
		t.Fatal(err)
	}

	rows, err := Stats(dest)
	if err != nil {
		t.Fatal(err)
	}
	want := []Stat{
		{ID: 8, HasID: true, When: "2024-03-09 08:32", Name: "8-2024-03-09-0832-sonoma", CSV: "yes", JSON: "no", Video: "optimal", Unique: "optimal"},
		{ID: 9, HasID: true, When: "2024-03-09 10:01", Track: "Sonoma Raceway", Car: "MAZDA MIATA", Name: "9-2024-03-09-1001-sonoma", CSV: "yes", JSON: "yes", Video: "no", Unique: "average"},
		{ID: 36, HasID: true, When: "2025-10-17 12:52", Track: "Sonoma Raceway", Car: "ACURA NSX", Name: "36-2025-10-17-1252-sonoma", CSV: "no", JSON: "bad", Video: "broken", Unique: "-"},
		{ID: 43, HasID: true, When: "2026-02-07 14:46", Track: "Sonoma Raceway", Car: "ACURA NSX", Name: "43-2026-02-07-1446-sonoma-raceway-acura-nsx", CSV: "yes", JSON: "yes", Video: "yes", Unique: "optimal, average"},
		{ID: 99, HasID: true, When: "2024-01-02 03:04", Track: "Laguna", Car: "Miata", Name: "99-missing", CSV: "no", JSON: "no", Video: "no", Unique: "-"},
		{HasID: false, Name: "notes", CSV: "no", JSON: "no", Video: "no", Unique: "-"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %d", len(rows))
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Fatalf("row %d\n got %#v\nwant %#v", i, rows[i], want[i])
		}
	}

	var buf bytes.Buffer
	if err := FormatStats(&buf, rows); err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"UNIQUE", "optimal, average", "broken", "notes"} {
		if !bytes.Contains(buf.Bytes(), []byte(needle)) {
			t.Fatalf("table missing %q:\n%s", needle, buf.String())
		}
	}
}

func TestStatsMissingLibrary(t *testing.T) {
	rows, err := Stats(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows %d", len(rows))
	}
	var buf bytes.Buffer
	if err := FormatStats(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "No packs found.\n" {
		t.Fatalf("empty %q", buf.String())
	}
}
