package catalyst

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

// Binary lap-data layout (little-endian):
//
//	header: 20 bytes; int32 record count at offset 4
//	record: 76 bytes each
const (
	headerSize = 20
	recordSize = 76
)

// TelemetryPoint is one decoded sample. Speed is in MPH as stored on the device;
// accelerations are in G; gyro rates in rad/s.
type TelemetryPoint struct {
	TimeOffset float64 // seconds since lap start
	Lat        float64
	Lon        float64
	SpeedMph   float64
	TrackUsage float64 // percent
	Altitude   float64 // meters
	AccelLong  float64 // G
	AccelLat   float64 // G
	AccelZ     float64 // G
	GyroX      float64 // rad/s
	GyroY      float64 // rad/s
	GyroZ      float64 // rad/s
}

// ParseLapFile decodes a binary lap-data file. lapDurationMs (if > 0) is used to
// derive the per-sample time step; otherwise a 0.1s (10 Hz) default is assumed.
func ParseLapFile(path string, lapDurationMs int64) ([]TelemetryPoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < headerSize {
		return nil, fmt.Errorf("lap file %s too small (%d bytes)", path, len(data))
	}

	// Trust the header count only up to what the file can actually hold, so a
	// corrupt or hostile count can't drive a huge allocation.
	count := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	if maxRecords := (len(data) - headerSize) / recordSize; count > maxRecords {
		count = maxRecords
	}
	if count < 0 {
		count = 0
	}

	timeStep := 0.1
	if lapDurationMs > 0 && count > 0 {
		timeStep = float64(lapDurationMs) / 1000.0 / float64(count)
	}

	points := make([]TelemetryPoint, 0, count)
	for i := 0; i < count; i++ {
		off := headerSize + i*recordSize
		if off+recordSize > len(data) {
			break // truncated file: keep complete records only
		}
		rec := data[off : off+recordSize]

		points = append(points, TelemetryPoint{
			TimeOffset: float64(i) * timeStep,
			Lat:        f64(rec, 0),
			Lon:        f64(rec, 8),
			SpeedMph:   float64(f32(rec, 16)),
			TrackUsage: float64(f32(rec, 28)) * 100,
			Altitude:   float64(f32(rec, 32)),
			AccelLong:  float64(f32(rec, 36)) / 9.81,
			AccelLat:   float64(f32(rec, 40)) / 9.81,
			AccelZ:     float64(f32(rec, 44)) / 9.81,
			GyroX:      float64(f32(rec, 48)),
			GyroY:      float64(f32(rec, 52)),
			GyroZ:      float64(f32(rec, 56)),
		})
	}
	return points, nil
}

func f64(b []byte, off int) float64 {
	return math.Float64frombits(binary.LittleEndian.Uint64(b[off : off+8]))
}

func f32(b []byte, off int) float32 {
	return math.Float32frombits(binary.LittleEndian.Uint32(b[off : off+4]))
}
