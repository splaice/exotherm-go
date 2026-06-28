// Package catalyst reads an extracted Catalyst bundle: the catalyst.db SQLite
// metadata and the binary lap telemetry files alongside it.
package catalyst

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// dataFolders are the per-session data directories produced by the parser; we
// index them so a lap's stored path can be resolved back to a local file.
var dataFolders = map[string]bool{
	"LapData":         true,
	"Meanlines":       true,
	"OptLapManifests": true,
	"RawAccel":        true,
	"RawGNSS":         true,
	"RawGyro":         true,
	"RawLatPos":       true,
	"VideoManifests":  true,
}

// Bundle is an opened extracted Catalyst directory.
type Bundle struct {
	Root       string
	dbPath     string
	db         *sql.DB
	folderDirs map[string]string // "LapData" -> absolute dir
	files      map[string]string // base name -> absolute path (first seen)
}

// Session is a row of the sessions listing, joined with track/car metadata.
type Session struct {
	ID                int64
	StartTimeUTCs     int64
	NumLaps           sql.NullInt64
	FastestLapTimeMs  sql.NullInt64
	MaxSpeedMps       sql.NullFloat64
	SessionDurationMs sql.NullInt64
	TrackName         sql.NullString
	Make              sql.NullString
	Model             sql.NullString
}

// Lap is a row of lap_data needed to locate and time its telemetry.
type Lap struct {
	ID             int64
	SessionID      int64
	LapNum         int64
	LapStartTimeMs int64
	LapDurationMs  int64
	InterpLoc      sql.NullString
}

// Open locates catalyst.db under root and indexes the data folders.
func Open(root string) (*Bundle, error) {
	b := &Bundle{Root: root, folderDirs: map[string]string{}, files: map[string]string{}}

	// Single pass: locate catalyst.db, index the data folders, and index every
	// file by base name so lap-data lookups never re-walk the tree.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if dataFolders[d.Name()] {
				if _, seen := b.folderDirs[d.Name()]; !seen {
					b.folderDirs[d.Name()] = path
				}
			}
			return nil
		}
		if d.Name() == "catalyst.db" && b.dbPath == "" {
			b.dbPath = path
		}
		if _, seen := b.files[d.Name()]; !seen {
			b.files[d.Name()] = path
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if b.dbPath == "" {
		return nil, fmt.Errorf("catalyst.db not found under %s", root)
	}

	// Open read-only; the extracted DB is a throwaway local copy.
	db, err := sql.Open("sqlite", "file:"+b.dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open catalyst.db: %w", err)
	}
	b.db = db
	return b, nil
}

func (b *Bundle) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

const sessionSelect = `
	SELECT
		sr.id,
		sr.session_start_time_utc_s,
		sr.num_laps,
		sr.fastest_lap_time_ms,
		sr.max_speed_mps,
		sr.session_duration_ms,
		f.name  AS track_name,
		c.make  AS make,
		c.model AS model
	FROM session_results sr
	LEFT JOIN session_configuration sc ON sr.id = sc.id
	LEFT JOIN track_configurations  tc ON sc.track_config_id = tc.id
	LEFT JOIN track_routes          tr ON tc.track_route_id  = tr.id
	LEFT JOIN facilities            f  ON tr.facility_id      = f.id
	LEFT JOIN cars                  c  ON sc.car_id           = c.id
`

// fallbackSelect is used when the optional join tables are absent (older dbs).
const fallbackSelect = `
	SELECT id, session_start_time_utc_s, num_laps, fastest_lap_time_ms,
	       max_speed_mps, session_duration_ms, NULL, NULL, NULL
	FROM session_results sr
`

// querySessions runs the joined session query with the given trailing clause,
// degrading to the bare session_results listing if a join table is absent.
func (b *Bundle) querySessions(clause string, args ...any) (*sql.Rows, error) {
	rows, err := b.db.Query(sessionSelect+clause, args...)
	if err != nil {
		return b.db.Query(fallbackSelect+clause, args...)
	}
	return rows, nil
}

// ListSessions returns all sessions, newest first.
func (b *Bundle) ListSessions() ([]Session, error) {
	rows, err := b.querySessions(" ORDER BY sr.session_start_time_utc_s DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSessions(rows)
}

// GetSession returns one session by id.
func (b *Bundle) GetSession(id string) (*Session, error) {
	rows, err := b.querySessions(" WHERE sr.id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions, err := scanSessions(rows)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, fmt.Errorf("session %s not found", id)
	}
	return &sessions[0], nil
}

func scanSessions(rows *sql.Rows) ([]Session, error) {
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.StartTimeUTCs, &s.NumLaps, &s.FastestLapTimeMs,
			&s.MaxSpeedMps, &s.SessionDurationMs, &s.TrackName, &s.Make, &s.Model); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Laps returns a session's laps ordered by lap number.
func (b *Bundle) Laps(sessionID string) ([]Lap, error) {
	rows, err := b.db.Query(
		`SELECT id, session_id, lap_num, lap_start_time_ms, lap_duration_ms, interp_lap_data_loc
		 FROM lap_data WHERE session_id = ? ORDER BY lap_num ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Lap
	for rows.Next() {
		var l Lap
		if err := rows.Scan(&l.ID, &l.SessionID, &l.LapNum, &l.LapStartTimeMs,
			&l.LapDurationMs, &l.InterpLoc); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// LapDataPath resolves a lap's stored interp_lap_data_loc to a local file.
//
// The file name is the last path segment and its data folder the second-to-last
// (defaulting to LapData). It is resolved against that folder when known, then
// against the by-name index built at Open time.
func (b *Bundle) LapDataPath(loc string) (string, error) {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return "", fmt.Errorf("empty lap data location")
	}
	parts := strings.Split(strings.ReplaceAll(loc, "\\", "/"), "/")
	fileName := parts[len(parts)-1]
	folder := "LapData"
	if len(parts) >= 2 {
		folder = parts[len(parts)-2]
	}

	// Prefer the file's own folder, then LapData, to disambiguate same-named
	// files across folders; otherwise fall back to the by-name index.
	for _, dir := range []string{b.folderDirs[folder], b.folderDirs["LapData"]} {
		if dir != "" {
			if p := filepath.Join(dir, fileName); fileExists(p) {
				return p, nil
			}
		}
	}
	if p := b.files[fileName]; p != "" {
		return p, nil
	}
	return "", fmt.Errorf("lap data file %q not found under %s", fileName, b.Root)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
