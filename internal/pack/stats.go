package pack

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// Stat is one stored pack. CSV and JSON are yes, no, or bad. Video is yes
// when video.mp4 plays, optimal when only optimal.mp4 plays, broken when a
// video file is truncated, and no when the pack has no video. Unique names
// the synthetic laps kept in the pack: optimal (lap 0, optimal.mp4, or
// optimal.csv) and average (lap -1).
type Stat struct {
	ID     int64
	HasID  bool
	When   string
	Track  string
	Car    string
	CSV    string
	JSON   string
	Video  string
	Unique string
	Name   string
}

// Stats reads dest/sessions and dest/index.yaml. A missing library is an empty
// list. An index entry whose directory is gone is still listed, with every
// file marked absent.
func Stats(dest string) ([]Stat, error) {
	idx, err := loadIndex(dest)
	if err != nil {
		return nil, err
	}
	byPath := map[string]*Entry{}
	for i := range idx.Sessions {
		byPath[idx.Sessions[i].Path] = &idx.Sessions[i]
	}

	sessionsDir := filepath.Join(dest, "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	seen := map[string]bool{}
	var rows []Stat
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("sessions", e.Name()))
		seen[rel] = true
		rows = append(rows, inspectDir(filepath.Join(sessionsDir, e.Name()), e.Name(), byPath[rel]))
	}
	for _, ent := range idx.Sessions {
		if seen[ent.Path] {
			continue
		}
		rows = append(rows, indexOnly(ent))
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].HasID != rows[j].HasID {
			return rows[i].HasID
		}
		if rows[i].ID != rows[j].ID {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

// FormatStats writes the pack table. An empty list prints a single line.
func FormatStats(w io.Writer, rows []Stat) error {
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "No packs found.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWHEN\tTRACK\tCAR\tCSV\tJSON\tVIDEO\tUNIQUE\tDIR")
	for _, r := range rows {
		id := "-"
		if r.HasID {
			id = strconv.FormatInt(r.ID, 10)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			id, dash(r.When), dash(r.Track), dash(r.Car), r.CSV, r.JSON, r.Video, r.Unique, dash(r.Name))
	}
	return tw.Flush()
}

func inspectDir(dir, name string, ent *Entry) Stat {
	st := Stat{Name: name, CSV: "no", JSON: "no", Video: "no", Unique: "-"}
	if id, ok := leadingID(name); ok {
		st.ID, st.HasID = id, true
	}
	if when, ok := dirWhen(name); ok {
		st.When = when
	}
	if ent != nil {
		applyEntry(&st, ent)
	}

	doc, err := readSessionFile(filepath.Join(dir, "session.json"))
	switch {
	case err == nil:
		st.JSON = "yes"
		st.ID, st.HasID = doc.SessionID, true
		if when := formatWhen(doc.When); when != "" {
			st.When = when
		}
		if doc.Track != "" {
			st.Track = doc.Track
		}
		if doc.Car != "" {
			st.Car = doc.Car
		}
	case os.IsNotExist(err):
		st.JSON = "no"
	default:
		st.JSON = "bad"
	}

	hasData, optimal, average, csvErr := csvLaps(filepath.Join(dir, "session.csv"))
	switch {
	case csvErr != nil && !os.IsNotExist(csvErr):
		st.CSV = "bad"
	case hasData:
		st.CSV = "yes"
	}
	if !optimal {
		optimal = optimalArtifact(dir)
	}
	st.Unique = uniqueLabel(optimal, average)
	st.Video = videoAvailable(dir)
	return st
}

func indexOnly(ent Entry) Stat {
	st := Stat{
		Name:   filepath.Base(ent.Path),
		CSV:    "no",
		JSON:   "no",
		Video:  "no",
		Unique: "-",
	}
	applyEntry(&st, &ent)
	return st
}

func applyEntry(st *Stat, ent *Entry) {
	st.ID, st.HasID = ent.SessionID, true
	if when := formatWhen(ent.When); when != "" {
		st.When = when
	}
	if ent.Track != "" {
		st.Track = ent.Track
	}
	if ent.Car != "" {
		st.Car = ent.Car
	}
	if ent.Path != "" {
		st.Name = filepath.Base(ent.Path)
	}
}

func optimalArtifact(dir string) bool {
	if classifyVideo(filepath.Join(dir, "optimal.mp4")) != videoMissing {
		return true
	}
	hasData, _, _, err := csvLaps(filepath.Join(dir, "optimal.csv"))
	return err == nil && hasData
}

func uniqueLabel(optimal, average bool) string {
	switch {
	case optimal && average:
		return "optimal, average"
	case optimal:
		return "optimal"
	case average:
		return "average"
	default:
		return "-"
	}
}

// videoAvailable reports the session recording first. A pack whose session
// file is missing or unplayable can still have the optimal-lap video.
func videoAvailable(dir string) string {
	session := classifyVideo(filepath.Join(dir, "video.mp4"))
	optimal := classifyVideo(filepath.Join(dir, "optimal.mp4"))
	switch {
	case session == videoOK:
		return "yes"
	case optimal == videoOK:
		return "optimal"
	case session == videoBroken || optimal == videoBroken:
		return "broken"
	default:
		return "no"
	}
}

// csvLaps reports whether path has a data row, and whether that file contains
// lap 0 (optimal) or lap -1 (average). A header-only file has no data.
func csvLaps(path string) (hasData, optimal, average bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, false, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	header := true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			return hasData, optimal, average, nil
		}
		if err != nil {
			return hasData, optimal, average, err
		}
		if len(rec) == 0 {
			continue
		}
		lap := strings.TrimSpace(rec[0])
		if header {
			header = false
			if strings.EqualFold(lap, "lap") {
				continue
			}
		}
		if lap == "" {
			continue
		}
		hasData = true
		switch lap {
		case "0":
			optimal = true
		case "-1":
			average = true
		}
	}
}

func leadingID(name string) (int64, bool) {
	head := name
	if i := strings.IndexByte(name, '-'); i > 0 {
		head = name[:i]
	}
	id, err := strconv.ParseInt(head, 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// dirWhen reads the YYYY-MM-DD-HHMM stamp sessionDirName writes after the id.
func dirWhen(name string) (string, bool) {
	i := strings.IndexByte(name, '-')
	if i <= 0 || i+1 >= len(name) {
		return "", false
	}
	rest := name[i+1:]
	if len(rest) < 15 || rest[4] != '-' || rest[7] != '-' || rest[10] != '-' {
		return "", false
	}
	if len(rest) > 15 && rest[15] != '-' {
		return "", false
	}
	date, hm := rest[:10], rest[11:15]
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return "", false
	}
	hh, errH := strconv.Atoi(hm[:2])
	mm, errM := strconv.Atoi(hm[2:])
	if errH != nil || errM != nil || hh > 23 || mm > 59 {
		return "", false
	}
	return date + " " + hm[:2] + ":" + hm[2:], true
}

func formatWhen(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Format("2006-01-02 15:04")
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
