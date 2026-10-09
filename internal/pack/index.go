package pack

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Entry is one session already stored under dest.
type Entry struct {
	GUID      string `yaml:"guid"`
	SessionID int64  `yaml:"session_id"`
	When      string `yaml:"when"`
	Track     string `yaml:"track,omitempty"`
	Car       string `yaml:"car,omitempty"`
	Path      string `yaml:"path"`
	Video     string `yaml:"video"` // ok, missing, or broken
	Imported  string `yaml:"imported"`
}

// Index is dest/index.yaml. Entries are kept sorted by session id.
type Index struct {
	Sessions []Entry `yaml:"sessions"`
}

func indexPath(dest string) string { return filepath.Join(dest, "index.yaml") }

func loadIndex(dest string) (Index, error) {
	raw, err := os.ReadFile(indexPath(dest))
	if err != nil {
		if os.IsNotExist(err) {
			return Index{}, nil
		}
		return Index{}, err
	}
	var idx Index
	if err := yaml.Unmarshal(raw, &idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

func (idx *Index) find(key string) *Entry {
	for i := range idx.Sessions {
		if idx.Sessions[i].GUID == key {
			return &idx.Sessions[i]
		}
	}
	return nil
}

func (idx *Index) upsert(e Entry) {
	for i := range idx.Sessions {
		if idx.Sessions[i].GUID == e.GUID {
			idx.Sessions[i] = e
			return
		}
	}
	idx.Sessions = append(idx.Sessions, e)
	sort.Slice(idx.Sessions, func(i, j int) bool {
		return idx.Sessions[i].SessionID < idx.Sessions[j].SessionID
	})
}

func saveIndex(dest string, idx Index) error {
	raw, err := yaml.Marshal(idx)
	if err != nil {
		return err
	}
	path := indexPath(dest)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
