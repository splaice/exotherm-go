package catalyst

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// writeLapFile builds a binary lap file with the given header count and records,
// each record provided as the 76 raw bytes.
func writeLapFile(t *testing.T, count int, records [][]byte) string {
	t.Helper()
	buf := make([]byte, headerSize)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(count))
	for _, r := range records {
		buf = append(buf, r...)
	}
	path := filepath.Join(t.TempDir(), "lap.bin")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// record builds one 76-byte record from the documented field offsets, writing
// the raw on-disk values (before the parser's unit conversions).
func record(lat, lon float64, speedMph, trackUsage, altitude, accelLong, accelLat, accelZ, gyroX, gyroY, gyroZ float32) []byte {
	r := make([]byte, recordSize)
	binary.LittleEndian.PutUint64(r[0:8], math.Float64bits(lat))
	binary.LittleEndian.PutUint64(r[8:16], math.Float64bits(lon))
	binary.LittleEndian.PutUint32(r[16:20], math.Float32bits(speedMph))
	binary.LittleEndian.PutUint32(r[28:32], math.Float32bits(trackUsage))
	binary.LittleEndian.PutUint32(r[32:36], math.Float32bits(altitude))
	binary.LittleEndian.PutUint32(r[36:40], math.Float32bits(accelLong))
	binary.LittleEndian.PutUint32(r[40:44], math.Float32bits(accelLat))
	binary.LittleEndian.PutUint32(r[44:48], math.Float32bits(accelZ))
	binary.LittleEndian.PutUint32(r[48:52], math.Float32bits(gyroX))
	binary.LittleEndian.PutUint32(r[52:56], math.Float32bits(gyroY))
	binary.LittleEndian.PutUint32(r[56:60], math.Float32bits(gyroZ))
	return r
}

func approx(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %v, want %v (±%v)", name, got, want, tol)
	}
}

// TestParseLapFileDecodesFields pins the binary layout: field offsets,
// little-endian decoding, and the unit conversions (G = raw/9.81,
// trackUsage = raw*100).
func TestParseLapFileDecodesFields(t *testing.T) {
	rec := record(
		36.5, -79.2, // lat, lon
		100.0,         // speed mph
		0.5,           // track usage -> *100 = 50
		250.0,         // altitude
		9.81,          // accelLong -> /9.81 = 1 G
		-19.62,        // accelLat -> -2 G
		4.905,         // accelZ -> 0.5 G
		0.1, 0.2, 0.3, // gyro
	)
	path := writeLapFile(t, 1, [][]byte{rec})

	pts, err := ParseLapFile(path, 0) // no duration -> 0.1s step
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1", len(pts))
	}
	p := pts[0]
	approx(t, "Lat", p.Lat, 36.5, 1e-9)
	approx(t, "Lon", p.Lon, -79.2, 1e-9)
	approx(t, "SpeedMph", p.SpeedMph, 100.0, 1e-3)
	approx(t, "TrackUsage", p.TrackUsage, 50.0, 1e-3)
	approx(t, "Altitude", p.Altitude, 250.0, 1e-3)
	approx(t, "AccelLong(G)", p.AccelLong, 1.0, 1e-4)
	approx(t, "AccelLat(G)", p.AccelLat, -2.0, 1e-4)
	approx(t, "AccelZ(G)", p.AccelZ, 0.5, 1e-4)
	approx(t, "GyroX", p.GyroX, 0.1, 1e-4)
	approx(t, "GyroY", p.GyroY, 0.2, 1e-4)
	approx(t, "GyroZ", p.GyroZ, 0.3, 1e-4)
	approx(t, "TimeOffset", p.TimeOffset, 0.0, 1e-9)
}

// TestParseLapFileTimeStep checks the time step is derived from lap duration and
// record count, and that offsets are monotonic.
func TestParseLapFileTimeStep(t *testing.T) {
	recs := make([][]byte, 4)
	for i := range recs {
		recs[i] = record(36.5, -79.2, 50, 0, 0, 0, 0, 0, 0, 0, 0)
	}
	path := writeLapFile(t, 4, recs)

	pts, err := ParseLapFile(path, 8000) // 8s over 4 records -> 2s step
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 4 {
		t.Fatalf("got %d points, want 4", len(pts))
	}
	for i, p := range pts {
		approx(t, "TimeOffset", p.TimeOffset, float64(i)*2.0, 1e-9)
	}
}

// TestParseLapFileTruncated ensures a header count larger than the available
// bytes keeps only the complete records present.
func TestParseLapFileTruncated(t *testing.T) {
	recs := [][]byte{
		record(36.5, -79.2, 50, 0, 0, 0, 0, 0, 0, 0, 0),
		record(36.5, -79.2, 51, 0, 0, 0, 0, 0, 0, 0, 0),
	}
	path := writeLapFile(t, 5, recs) // claims 5, only 2 present

	pts, err := ParseLapFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("got %d points, want 2 (truncated)", len(pts))
	}
}

// TestParseLapFileHugeCount ensures a hostile/corrupt header count cannot drive
// a huge allocation: the count is capped to what the file can actually hold.
func TestParseLapFileHugeCount(t *testing.T) {
	rec := record(36.5, -79.2, 50, 0, 0, 0, 0, 0, 0, 0, 0)
	path := writeLapFile(t, 1_000_000_000, [][]byte{rec}) // claims 1e9, one present

	pts, err := ParseLapFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 1 {
		t.Fatalf("got %d points, want 1 (count capped to available)", len(pts))
	}
}

// TestParseLapFileTooSmall rejects a file smaller than the header.
func TestParseLapFileTooSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tiny.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseLapFile(path, 0); err == nil {
		t.Fatal("expected error for undersized file")
	}
}
