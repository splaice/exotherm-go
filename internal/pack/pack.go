package pack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/exotherm/exotherm-go/internal/archive"
	"github.com/exotherm/exotherm-go/internal/catalyst"
	"github.com/exotherm/exotherm-go/internal/export"
)

// Options controls one pack run. Dest, Tmp, and Speed are already resolved.
type Options struct {
	Card       string
	Dest       string
	Tmp        string
	Speed      export.SpeedUnit
	Cleanup    bool
	Passphrase string
	Out        io.Writer
}

// Summary is the last line pack prints.
type Summary struct {
	Packed  int
	Added   int
	Skipped int
	Missing int
	Broken  int
	Ignored int
}

func (s Summary) String() string {
	msg := fmt.Sprintf("packed %d, added video %d, skipped %d, missing video %d, broken video %d",
		s.Packed, s.Added, s.Skipped, s.Missing, s.Broken)
	if s.Ignored > 0 {
		msg += fmt.Sprintf(", ignored %d empty", s.Ignored)
	}
	return msg
}

// Run reads the card, writes one directory per session under Dest, and updates
// Dest/index.yaml. Tmp holds the decrypted log and is removed when Cleanup is set.
func Run(opt Options) (Summary, error) {
	out := opt.Out
	if out == nil {
		out = os.Stdout
	}
	if opt.Card == "" {
		card, err := FindCard()
		if err != nil {
			return Summary{}, err
		}
		opt.Card = card
	}
	fmt.Fprintf(out, "reading %s\n", opt.Card)

	if err := os.MkdirAll(opt.Dest, 0o755); err != nil {
		return Summary{}, err
	}
	if err := os.MkdirAll(opt.Tmp, 0o755); err != nil {
		return Summary{}, err
	}
	defer func() {
		if opt.Cleanup {
			os.RemoveAll(opt.Tmp)
		}
	}()

	logs, err := supportLogsDir(opt.Card)
	if err != nil {
		return Summary{}, err
	}
	parts, err := archive.CollectParts(logs)
	if err != nil {
		return Summary{}, err
	}
	fmt.Fprintf(out, "decrypting %d SupportLogs part(s)\n", len(parts))
	bundleDir := filepath.Join(opt.Tmp, "bundle")
	if err := os.RemoveAll(bundleDir); err != nil {
		return Summary{}, err
	}
	res, err := archive.Extract(parts, opt.Passphrase, bundleDir)
	if err != nil {
		return Summary{}, err
	}
	if res.PartialRecovery {
		fmt.Fprintf(out, "warning: partial recovery — %v\n", res.Notes)
	}
	b, err := catalyst.Open(bundleDir)
	if err != nil {
		return Summary{}, err
	}
	defer b.Close()

	sessions, err := b.ListSessions()
	if err != nil {
		return Summary{}, err
	}
	idx, err := loadIndex(opt.Dest)
	if err != nil {
		return Summary{}, err
	}

	var summary Summary
	var packErrs []error
	now := time.Now().Format(time.RFC3339)
	for _, s := range sessions {
		if s.StartTimeUTCs <= 0 || !s.NumLaps.Valid || s.NumLaps.Int64 <= 0 {
			summary.Ignored++
			continue
		}
		extra, err := b.SessionExtra(s.ID)
		if err != nil {
			packErrs = append(packErrs, err)
			continue
		}
		key := extra.GUID
		if key == "" {
			key = "id:" + strconv.FormatInt(s.ID, 10)
		}
		label := sessionLabel(s, extra.UTCOffsetS)
		when := sessionTime(s.StartTimeUTCs, extra.UTCOffsetS)
		dirName := sessionDirName(s, extra.UTCOffsetS)
		rel := filepath.Join("sessions", dirName)
		dir := filepath.Join(opt.Dest, rel)

		manifestPath, offsetMs, hasManifest, err := b.VideoManifest(s.ID)
		if err != nil {
			packErrs = append(packErrs, err)
			continue
		}
		cardVideo := ""
		if hasManifest {
			cardVideo = filepath.Join(opt.Card, filepath.FromSlash(manifestPath))
		}
		state := classifyVideo(cardVideo)
		entry := idx.find(key)
		action := decide(entry, dir, state)

		switch action {
		case actSkip:
			fmt.Fprintf(out, "identified prior pack, skipping %s\n", label)
			summary.Skipped++
			continue
		case actAddVideo:
			fmt.Fprintf(out, "identified prior pack, adding video %s\n", label)
			if err := copyFile(filepath.Join(dir, "video.mp4"), cardVideo); err != nil {
				packErrs = append(packErrs, fmt.Errorf("session %d video: %w", s.ID, err))
				continue
			}
			doc, err := readSessionFile(filepath.Join(dir, "session.json"))
			if err != nil {
				doc = sessionDoc(s, extra, when, offsetMs, hasManifest, videoOK)
			}
			doc.Video = "video.mp4"
			doc.VideoStatus = string(videoOK)
			if err := writeSessionFile(dir, doc); err != nil {
				packErrs = append(packErrs, err)
				continue
			}
			idx.upsert(makeEntry(key, s, when, rel, videoOK, now))
			if err := saveIndex(opt.Dest, idx); err != nil {
				return summary, err
			}
			summary.Added++
			continue
		}

		fmt.Fprintf(out, "identified new pack, extracting %s\n", label)
		if err := os.RemoveAll(dir); err != nil {
			packErrs = append(packErrs, err)
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			packErrs = append(packErrs, err)
			continue
		}
		laps, err := b.Laps(strconv.FormatInt(s.ID, 10))
		if err != nil {
			packErrs = append(packErrs, err)
			continue
		}
		if err := writeCSV(filepath.Join(dir, "session.csv"), b, laps, opt.Speed); err != nil {
			packErrs = append(packErrs, fmt.Errorf("session %d: %w", s.ID, err))
			continue
		}
		switch state {
		case videoOK:
			if err := copyFile(filepath.Join(dir, "video.mp4"), cardVideo); err != nil {
				packErrs = append(packErrs, fmt.Errorf("session %d video: %w", s.ID, err))
				continue
			}
			summary.Packed++
		case videoMissing:
			summary.Packed++
			summary.Missing++
		case videoBroken:
			summary.Packed++
			summary.Broken++
		}
		doc := sessionDoc(s, extra, when, offsetMs, hasManifest, state)
		if state == videoOK {
			doc.Video = "video.mp4"
		}
		if lap1, ok := lap1Seconds(laps); ok {
			doc.Lap1TimeS = &lap1
		}
		if optPath := cardPath(opt.Card, extra.OptVideo); optPath != "" && classifyVideo(optPath) == videoOK {
			if err := copyFile(filepath.Join(dir, "optimal.mp4"), optPath); err != nil {
				packErrs = append(packErrs, fmt.Errorf("session %d optimal video: %w", s.ID, err))
				continue
			}
			doc.Optimal = "optimal.mp4"
		}
		if optCSV, ok := b.OptManifestPath(s.ID); ok {
			if err := copyFile(filepath.Join(dir, "optimal.csv"), optCSV); err != nil {
				packErrs = append(packErrs, err)
				continue
			}
		}
		if err := writeSessionFile(dir, doc); err != nil {
			packErrs = append(packErrs, err)
			continue
		}
		idx.upsert(makeEntry(key, s, when, rel, state, now))
		if err := saveIndex(opt.Dest, idx); err != nil {
			return summary, err
		}
	}
	fmt.Fprintln(out, summary.String())
	return summary, errors.Join(packErrs...)
}

type action int

const (
	actPack action = iota
	actSkip
	actAddVideo
)

// decide chooses what to do with a session that already has an index entry.
// A prior pack whose video is still missing stays skipped. A prior pack whose
// video is on this card, and was not copied before, gets the file added.
func decide(entry *Entry, dir string, card videoState) action {
	csv := filepath.Join(dir, "session.csv")
	haveCSV := fileExists(csv)
	if entry == nil || !haveCSV {
		return actPack
	}
	haveVideo := fileExists(filepath.Join(dir, "video.mp4"))
	if entry.Video == string(videoOK) {
		if haveVideo {
			return actSkip
		}
		return actPack
	}
	if card == videoOK && !haveVideo {
		return actAddVideo
	}
	return actSkip
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// FindCard returns the single mounted volume that holds SupportLogs.
func FindCard() (string, error) {
	roots := []string{"/Volumes", "/media", "/mnt"}
	var hits []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(root, e.Name())
			if _, err := supportLogsDir(p); err == nil {
				hits = append(hits, p)
			}
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no Catalyst card found (looked for SupportLogs under /Volumes, /media, and /mnt)")
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("multiple Catalyst cards: %s", strings.Join(hits, ", "))
	}
}

func supportLogsDir(card string) (string, error) {
	for _, dir := range []string{filepath.Join(card, "Garmin"), card} {
		matches, _ := filepath.Glob(filepath.Join(dir, "SupportLogs*"))
		if len(matches) > 0 {
			return dir, nil
		}
	}
	return "", fmt.Errorf("no SupportLogs on %s", card)
}

func sessionTime(startUTC, offset int64) time.Time {
	loc := time.FixedZone("", int(offset))
	return time.Unix(startUTC, 0).In(loc)
}

func sessionLabel(s catalyst.Session, offset int64) string {
	t := sessionTime(s.StartTimeUTCs, offset)
	name := strings.TrimSpace(trackName(s) + " " + carName(s))
	if name == "" {
		return fmt.Sprintf("%d %s", s.ID, t.Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf("%d %s %s", s.ID, t.Format("2006-01-02 15:04"), name)
}

func sessionDirName(s catalyst.Session, offset int64) string {
	t := sessionTime(s.StartTimeUTCs, offset)
	parts := []string{strconv.FormatInt(s.ID, 10), t.Format("2006-01-02-1504")}
	if track := slug(trackName(s)); track != "" {
		parts = append(parts, track)
	}
	if car := slug(carName(s)); car != "" {
		parts = append(parts, car)
	}
	return strings.Join(parts, "-")
}

func trackName(s catalyst.Session) string {
	if s.TrackName.Valid {
		return s.TrackName.String
	}
	return ""
}

func carName(s catalyst.Session) string {
	var parts []string
	if s.Make.Valid && s.Make.String != "" {
		parts = append(parts, s.Make.String)
	}
	if s.Model.Valid && s.Model.String != "" {
		parts = append(parts, s.Model.String)
	}
	return strings.Join(parts, " ")
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

type sessionJSON struct {
	GUID         string   `json:"guid,omitempty"`
	SessionID    int64    `json:"session_id"`
	When         string   `json:"when"`
	Track        string   `json:"track,omitempty"`
	Car          string   `json:"car,omitempty"`
	VideoOffsetS *float64 `json:"video_offset_s,omitempty"`
	Lap1TimeS    *float64 `json:"lap1_time_s,omitempty"`
	Video        string   `json:"video,omitempty"`
	VideoStatus  string   `json:"video_status"`
	Optimal      string   `json:"optimal,omitempty"`
}

func sessionDoc(s catalyst.Session, extra catalyst.SessionExtra, when time.Time, offsetMs int64, hasManifest bool, state videoState) sessionJSON {
	doc := sessionJSON{
		GUID:        extra.GUID,
		SessionID:   s.ID,
		When:        when.Format(time.RFC3339),
		Track:       trackName(s),
		Car:         carName(s),
		VideoStatus: string(state),
	}
	if hasManifest {
		sec := float64(offsetMs) / 1000
		doc.VideoOffsetS = &sec
	}
	return doc
}

func lap1Seconds(laps []catalyst.Lap) (float64, bool) {
	for _, lap := range laps {
		if lap.LapNum == 1 {
			return float64(lap.LapStartTimeMs) / 1000, true
		}
	}
	return 0, false
}

func makeEntry(key string, s catalyst.Session, when time.Time, rel string, state videoState, imported string) Entry {
	return Entry{
		GUID:      key,
		SessionID: s.ID,
		When:      when.Format(time.RFC3339),
		Track:     trackName(s),
		Car:       carName(s),
		Path:      filepath.ToSlash(rel),
		Video:     string(state),
		Imported:  imported,
	}
}

func writeSessionFile(dir string, doc sessionJSON) error {
	// Lap 1's CSV Time is filled by the caller when it has the laps. sessionDoc
	// leaves it empty; writeSessionFile is also used by the add-video path,
	// which preserves an existing lap1 time.
	if doc.Lap1TimeS == nil {
		if prev, err := readSessionFile(filepath.Join(dir, "session.json")); err == nil {
			doc.Lap1TimeS = prev.Lap1TimeS
		}
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "session.json"), raw, 0o644)
}

func readSessionFile(path string) (sessionJSON, error) {
	var doc sessionJSON
	raw, err := os.ReadFile(path)
	if err != nil {
		return doc, err
	}
	err = json.Unmarshal(raw, &doc)
	return doc, err
}

func writeCSV(path string, b *catalyst.Bundle, laps []catalyst.Lap, unit export.SpeedUnit) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = export.SessionCSV(f, b, laps, unit)
	return err
}

func cardPath(card, loc string) string {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return ""
	}
	loc = strings.ReplaceAll(loc, "\\", "/")
	if strings.HasPrefix(loc, "Android/") || strings.HasPrefix(loc, "Garmin/") {
		return filepath.Join(card, filepath.FromSlash(loc))
	}
	return ""
}
