package catalyst

import (
	"encoding/binary"
	"math"
	"os"
	"strconv"
	"testing"

	"github.com/exotherm/exotherm-go/internal/archive"
)

// demoPassphrase / demoFixture: the bundled example archive (see the README).
const (
	demoPassphrase = "FF59E1881B4ED01C5C2F5F01890AFF25E9764D27"
	demoFixture    = "../../testdata/demo/SupportLogs.tar.gz.gpg.aa"
)

// demo holds the extracted demo bundle, opened once for all tests.
var demo *Bundle

// bbox is a loose lat/lon bounding box for a track.
type bbox struct{ latMin, latMax, lonMin, lonMax float64 }

// trackBoxes maps demo session IDs to their track's bounding box.
var trackBoxes = map[int64]bbox{
	124: {36.40, 36.50, -77.65, -77.50}, // NCCAR
	64:  {36.50, 36.60, -79.30, -79.10}, // Virginia International Raceway
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "catalyst-test-")
	if err != nil {
		panic(err)
	}
	parts, err := archive.CollectParts(demoFixture)
	if err != nil {
		panic(err)
	}
	if _, err := archive.Extract(parts, demoPassphrase, dir); err != nil {
		panic(err)
	}
	demo, err = Open(dir)
	if err != nil {
		panic(err)
	}

	code := m.Run()

	demo.Close()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestListSessions(t *testing.T) {
	sessions, err := demo.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	// Newest first.
	if sessions[0].ID != 124 || sessions[1].ID != 64 {
		t.Errorf("order = [%d, %d], want [124, 64]", sessions[0].ID, sessions[1].ID)
	}

	byID := map[int64]Session{}
	for _, s := range sessions {
		byID[s.ID] = s
	}
	if got := byID[124].TrackName.String; got != "NCCAR" {
		t.Errorf("session 124 track = %q, want NCCAR", got)
	}
	if got := byID[64].TrackName.String; got != "Virginia International Raceway" {
		t.Errorf("session 64 track = %q, want Virginia International Raceway", got)
	}
	if got := byID[124].Make.String; got != "LOTUS" {
		t.Errorf("session 124 make = %q, want LOTUS", got)
	}
}

func TestGetSession(t *testing.T) {
	s, err := demo.GetSession("64")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 64 {
		t.Errorf("ID = %d, want 64", s.ID)
	}
	if _, err := demo.GetSession("999999"); err == nil {
		t.Error("expected error for missing session")
	}
}

func TestLaps(t *testing.T) {
	laps, err := demo.Laps("124")
	if err != nil {
		t.Fatal(err)
	}
	// Demo session 124 has out/in (avg, opt) plus two timed laps.
	gotNums := map[int64]bool{}
	for _, l := range laps {
		gotNums[l.LapNum] = true
		if l.SessionID != 124 {
			t.Errorf("lap %d has session_id %d, want 124", l.ID, l.SessionID)
		}
	}
	for _, want := range []int64{-1, 0, 1, 2} {
		if !gotNums[want] {
			t.Errorf("missing lap_num %d", want)
		}
	}
}

// TestLapDataParsing resolves and parses every lap file referenced by both demo
// sessions, asserting the decoded telemetry is self-consistent and the GPS trace
// lands on the right track.
func TestLapDataParsing(t *testing.T) {
	for sessionID, box := range trackBoxes {
		laps, err := demo.Laps(strconv.FormatInt(sessionID, 10))
		if err != nil {
			t.Fatalf("session %d laps: %v", sessionID, err)
		}
		if len(laps) == 0 {
			t.Fatalf("session %d has no laps", sessionID)
		}

		for _, lap := range laps {
			if !lap.InterpLoc.Valid || lap.InterpLoc.String == "" {
				continue
			}
			path, err := demo.LapDataPath(lap.InterpLoc.String)
			if err != nil {
				t.Errorf("session %d lap %d: resolve path: %v", sessionID, lap.LapNum, err)
				continue
			}

			pts, err := ParseLapFile(path, lap.LapDurationMs)
			if err != nil {
				t.Errorf("session %d lap %d: parse: %v", sessionID, lap.LapNum, err)
				continue
			}

			// Point count must match the file's own header count.
			if want := headerCount(t, path); len(pts) != want {
				t.Errorf("session %d lap %d: %d points, header says %d", sessionID, lap.LapNum, len(pts), want)
			}
			if len(pts) == 0 {
				t.Errorf("session %d lap %d: no telemetry points", sessionID, lap.LapNum)
				continue
			}

			var prev float64 = -1
			for i, p := range pts {
				if p.Lat < box.latMin || p.Lat > box.latMax || p.Lon < box.lonMin || p.Lon > box.lonMax {
					t.Errorf("session %d lap %d point %d: (%.5f, %.5f) outside track box", sessionID, lap.LapNum, i, p.Lat, p.Lon)
					break
				}
				if p.SpeedMph < 0 || p.SpeedMph > 300 {
					t.Errorf("session %d lap %d point %d: implausible speed %.2f mph", sessionID, lap.LapNum, i, p.SpeedMph)
					break
				}
				if math.IsNaN(p.Lat) || math.IsNaN(p.SpeedMph) {
					t.Errorf("session %d lap %d point %d: NaN value", sessionID, lap.LapNum, i)
					break
				}
				if p.TimeOffset < prev {
					t.Errorf("session %d lap %d: time offset not monotonic at point %d", sessionID, lap.LapNum, i)
					break
				}
				prev = p.TimeOffset
			}

			// Last offset should span roughly the lap duration (timed laps only).
			if lap.LapNum >= 1 && lap.LapDurationMs > 0 {
				last := pts[len(pts)-1].TimeOffset
				wantSpan := float64(lap.LapDurationMs) / 1000.0
				if last < wantSpan*0.5 || last > wantSpan {
					t.Errorf("session %d lap %d: last offset %.2fs, want ~%.2fs", sessionID, lap.LapNum, last, wantSpan)
				}
			}
		}
	}
}

func TestLapDataPathDefaultsToLapData(t *testing.T) {
	// A bare file name (no folder) should resolve against the LapData dir.
	if _, err := demo.LapDataPath("124_1.bin"); err != nil {
		t.Errorf("resolve bare name: %v", err)
	}
	if _, err := demo.LapDataPath("does_not_exist.bin"); err == nil {
		t.Error("expected error for missing file")
	}
}

// headerCount reads the int32 record count from a lap file's header.
func headerCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < headerSize {
		return 0
	}
	return int(int32(binary.LittleEndian.Uint32(data[4:8])))
}
