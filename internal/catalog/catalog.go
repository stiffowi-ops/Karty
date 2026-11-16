package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Location struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Filename       string   `json:"filename"`
	Category       string   `json:"category"`
	Description    string   `json:"description,omitempty"`
	Rating         string   `json:"rating,omitempty"`
	Latitude       *float64 `json:"lat,omitempty"`
	Longitude      *float64 `json:"lon,omitempty"`
	ImageExt       string   `json:"image_ext,omitempty"`
	ImageURL       string   `json:"image_url,omitempty"`
	SourceModified string   `json:"source_modified,omitempty"`
}

type Override struct {
	Title       *string  `json:"title,omitempty"`
	Category    *string  `json:"category,omitempty"`
	Description *string  `json:"description,omitempty"`
	Rating      *string  `json:"rating,omitempty"`
	Latitude    *float64 `json:"lat,omitempty"`
	Longitude   *float64 `json:"lon,omitempty"`
}

type Store struct {
	mu                              sync.RWMutex
	locations                       []Location
	dataDir, seedPath, overridePath string
}

func New(dataDir, seedPath, overridePath string) (*Store, error) {
	s := &Store{dataDir: dataDir, seedPath: seedPath, overridePath: overridePath}
	if err := os.MkdirAll(filepath.Join(dataDir, "images"), 0755); err != nil {
		return nil, err
	}
	return s, s.Reload()
}

func (s *Store) BasePath() string { return filepath.Join(s.dataDir, "catalog.json") }
func (s *Store) DataDir() string  { return s.dataDir }

func ReadFile(path string) ([]Location, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var loc []Location
	if err = json.Unmarshal(data, &loc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return loc, nil
}

func (s *Store) Base() ([]Location, error) {
	items, err := ReadFile(s.BasePath())
	if err == nil {
		return items, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return ReadFile(s.seedPath)
}

func (s *Store) SaveBase(loc []Location) error {
	sort.Slice(loc, func(i, j int) bool { return loc[i].ID < loc[j].ID })
	b, err := json.MarshalIndent(loc, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dataDir, ".catalog-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0644); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), s.BasePath())
}

func (s *Store) Reload() error {
	loc, err := s.Base()
	if err != nil {
		return err
	}
	override := map[string]Override{}
	b, err := os.ReadFile(s.overridePath)
	if err == nil {
		if err = json.Unmarshal(b, &override); err != nil {
			return fmt.Errorf("invalid overrides JSON: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := range loc {
		l := &loc[i]
		l.ImageURL = ""
		if o, ok := override[l.ID]; ok {
			if o.Title != nil {
				l.Title = *o.Title
			}
			if o.Category != nil {
				l.Category = *o.Category
			}
			if o.Description != nil {
				l.Description = *o.Description
			}
			if o.Rating != nil {
				l.Rating = *o.Rating
			}
			if o.Latitude != nil && *o.Latitude >= -90 && *o.Latitude <= 90 {
				l.Latitude = o.Latitude
			}
			if o.Longitude != nil && *o.Longitude >= -180 && *o.Longitude <= 180 {
				l.Longitude = o.Longitude
			}
		}
		if l.ImageExt == "" {
			l.ImageExt = strings.ToLower(filepath.Ext(l.Filename))
		}
		if validID(l.ID) && validExt(l.ImageExt) {
			name := l.ID + l.ImageExt
			if st, e := os.Stat(filepath.Join(s.dataDir, "images", name)); e == nil && st.Mode().IsRegular() {
				l.ImageURL = "/media/" + name
			}
		}
	}
	sort.Slice(loc, func(i, j int) bool { return loc[i].ID < loc[j].ID })
	s.mu.Lock()
	s.locations = loc
	s.mu.Unlock()
	return nil
}

func validID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func ValidID(id string) bool { return validID(id) }
func validExt(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}
func ValidExt(ext string) bool { return validExt(ext) }

func (s *Store) List() []Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Location, len(s.locations))
	copy(out, s.locations)
	return out
}
func (s *Store) Find(id string) (Location, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.locations {
		if l.ID == id {
			return l, true
		}
	}
	return Location{}, false
}

func Normalize(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "ё", "е"))
	for _, p := range []string{"метро", "станция", "м.", "№", "«", "»", "\""} {
		s = strings.ReplaceAll(s, p, " ")
	}
	return strings.Join(strings.Fields(s), " ")
}

func Search(locs []Location, text string) []Location {
	needle := Normalize(text)
	if needle == "" {
		return locs
	}
	words := strings.Fields(needle)
	var found []Location
	for _, l := range locs {
		field := Normalize(l.Title + " " + l.Category + " " + l.Description)
		hit := true
		for _, w := range words {
			if !strings.Contains(field, w) {
				hit = false
				break
			}
		}
		if hit {
			found = append(found, l)
		}
	}
	return found
}
