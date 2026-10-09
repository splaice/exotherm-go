package catalyst

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SessionExtra is the pack metadata that the sessions listing does not carry.
// Fields stay zero when an older database does not have the column.
type SessionExtra struct {
	GUID       string
	UTCOffsetS int64
	OptVideo   string // path on the Catalyst card, relative to the card root
}

// SessionExtra returns the GUID, local-time offset, and optimal-video path for
// one session. A database without those columns yields a zero extra.
func (b *Bundle) SessionExtra(id int64) (SessionExtra, error) {
	var guid, opt sql.NullString
	var offset sql.NullInt64
	err := b.db.QueryRow(
		`SELECT guid, utc_to_local_time_offset_s, opt_video_loc FROM session_results WHERE id = ?`, id,
	).Scan(&guid, &offset, &opt)
	if err != nil {
		if err == sql.ErrNoRows {
			return SessionExtra{}, fmt.Errorf("session %d not found", id)
		}
		if strings.Contains(err.Error(), "no such column") {
			return SessionExtra{}, nil
		}
		return SessionExtra{}, err
	}
	return SessionExtra{GUID: guid.String, UTCOffsetS: offset.Int64, OptVideo: opt.String}, nil
}

// VideoManifest reads VideoManifests/<id>.txt. The file is one line,
// "<card path>,<offset ms>", where the offset is milliseconds from the start
// of video_1.mp4 to the start of lap 1.
func (b *Bundle) VideoManifest(sessionID int64) (cardPath string, offsetMs int64, ok bool, err error) {
	dir := b.folderDirs["VideoManifests"]
	if dir == "" {
		return "", 0, false, nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, strconv.FormatInt(sessionID, 10)+".txt"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, false, nil
		}
		return "", 0, false, err
	}
	line := strings.TrimSpace(string(raw))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", 0, false, nil
	}
	path, ms, found := strings.Cut(line, ",")
	if !found {
		return "", 0, false, fmt.Errorf("session %d video manifest %q has no offset", sessionID, line)
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(ms), 10, 64)
	if err != nil {
		return "", 0, false, fmt.Errorf("session %d video manifest offset: %w", sessionID, err)
	}
	return strings.TrimSpace(path), offset, true, nil
}

// OptManifestPath returns the on-disk optimal-lap stitch list for a session.
func (b *Bundle) OptManifestPath(sessionID int64) (string, bool) {
	dir := b.folderDirs["OptLapManifests"]
	if dir == "" {
		return "", false
	}
	p := filepath.Join(dir, strconv.FormatInt(sessionID, 10)+".csv")
	if fileExists(p) {
		return p, true
	}
	return "", false
}
