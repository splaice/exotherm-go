// Package export writes a session's laps and telemetry to CSV.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	"github.com/exotherm/exotherm-go/internal/catalyst"
)

// SpeedUnit selects how the stored MPH speed is rendered in the CSV.
type SpeedUnit string

const (
	MetersPerSecond SpeedUnit = "m/s"
	MilesPerHour    SpeedUnit = "mph"
	KmPerHour       SpeedUnit = "kph"
)

// ParseSpeedUnit validates a speed-unit string, keeping the valid set and its
// error message in one place alongside the type.
func ParseSpeedUnit(s string) (SpeedUnit, error) {
	switch u := SpeedUnit(s); u {
	case MetersPerSecond, MilesPerHour, KmPerHour:
		return u, nil
	default:
		return "", fmt.Errorf("invalid speed unit %q (want m/s, mph, or kph)", s)
	}
}

func (u SpeedUnit) convert(mph float64) float64 {
	switch u {
	case MetersPerSecond:
		return mph * 0.44704
	case KmPerHour:
		return mph * 1.60934
	default: // mph
		return mph
	}
}

// Stats summarizes what was written.
type Stats struct {
	Laps    int
	Rows    int
	Skipped int // laps with no/unreadable telemetry
}

// SessionCSV writes every lap's telemetry for a session to w.
func SessionCSV(w io.Writer, b *catalyst.Bundle, laps []catalyst.Lap, unit SpeedUnit) (*Stats, error) {
	cw := csv.NewWriter(w)
	header := []string{
		"Lap", "Time", "Lap Time", "Latitude", "Longitude",
		fmt.Sprintf("Speed (%s)", unit),
		"Accel Long (G)", "Accel Lat (G)", "Accel Z (G)",
		"Gyro X (rad/s)", "Gyro Y (rad/s)", "Gyro Z (rad/s)",
		"Track Usage (%)", "Altitude (m)",
	}
	if err := cw.Write(header); err != nil {
		return nil, err
	}

	stats := &Stats{}
	row := make([]string, len(header)) // reused per point; csv.Write copies fields
	for _, lap := range laps {
		if !lap.InterpLoc.Valid || lap.InterpLoc.String == "" {
			stats.Skipped++
			continue
		}
		path, err := b.LapDataPath(lap.InterpLoc.String)
		if err != nil {
			stats.Skipped++
			continue
		}
		points, err := catalyst.ParseLapFile(path, lap.LapDurationMs)
		if err != nil {
			stats.Skipped++
			continue
		}

		lapStartS := float64(lap.LapStartTimeMs) / 1000.0
		for _, p := range points {
			sessionTime := lapStartS + p.TimeOffset
			row[0] = strconv.FormatInt(lap.LapNum, 10)
			row[1] = f(sessionTime, 3)
			row[2] = f(p.TimeOffset, 3)
			row[3] = f(p.Lat, 7)
			row[4] = f(p.Lon, 7)
			row[5] = f(unit.convert(p.SpeedMph), 3)
			row[6] = f(p.AccelLong, 3)
			row[7] = f(p.AccelLat, 3)
			row[8] = f(p.AccelZ, 3)
			row[9] = f(p.GyroX, 4)
			row[10] = f(p.GyroY, 4)
			row[11] = f(p.GyroZ, 4)
			row[12] = f(p.TrackUsage, 1)
			row[13] = f(p.Altitude, 1)
			if err := cw.Write(row); err != nil {
				return stats, err
			}
			stats.Rows++
		}
		stats.Laps++
	}

	cw.Flush()
	return stats, cw.Error()
}

func f(v float64, prec int) string {
	return strconv.FormatFloat(v, 'f', prec, 64)
}
