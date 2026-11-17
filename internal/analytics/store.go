package analytics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	View            = "view"
	FavoriteAdded   = "favorite_add"
	FavoriteRemoved = "favorite_remove"
)

type Event struct {
	At         time.Time `json:"at"`
	UserID     int64     `json:"user_id"`
	LocationID string    `json:"location_id"`
	Kind       string    `json:"kind"`
}

type Store struct {
	mu     sync.RWMutex
	path   string
	events []Event
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var e Event
		if err = json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("malformed analytics: %w", err)
		}
		if err = e.Validate(); err != nil {
			return nil, err
		}
		s.events = append(s.events, e)
	}
	return s, scanner.Err()
}
func (e Event) Validate() error {
	if e.UserID <= 0 || len(e.LocationID) < 1 || len(e.LocationID) > 80 || e.At.IsZero() {
		return errors.New("invalid analytics event")
	}
	switch e.Kind {
	case View, FavoriteAdded, FavoriteRemoved:
		return nil
	default:
		return errors.New("invalid analytics event kind")
	}
}
func (s *Store) Record(id int64, locID, kind string, at time.Time) error {
	if s == nil {
		return nil
	}
	e := Event{At: at.UTC(), UserID: id, LocationID: locID, Kind: kind}
	if err := e.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind == View {
		// Prevent favorites-toggle refreshes and repeated taps from inflating views.
		for i := len(s.events) - 1; i >= 0 && i >= len(s.events)-250; i-- {
			old := s.events[i]
			if old.At.Before(e.At.Add(-20 * time.Second)) {
				break
			}
			if old.Kind == View && old.UserID == id && old.LocationID == locID {
				return nil
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(e)
	_, err = f.Write(append(raw, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	s.events = append(s.events, e)
	return nil
}

type Count struct {
	LocationID       string
	Views            int
	UniqueViews      int
	Additions        int
	Removals         int
	CurrentFavorites int
	Leads            int
}

func (s *Store) Snapshot() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Event(nil), s.events...)
}
func (s *Store) Rankings(now time.Time, days int, kind string, allIDs []string, current map[string]int, leads map[string][]time.Time) []Count {
	scores := map[string]*Count{}
	for _, id := range allIDs {
		scores[id] = &Count{LocationID: id, CurrentFavorites: current[id]}
	}
	cutoff := time.Time{}
	if days > 0 {
		cutoff = now.AddDate(0, 0, -days)
	}
	unique := map[string]map[int64]struct{}{}
	for _, event := range s.Snapshot() {
		if event.At.After(now) || (!cutoff.IsZero() && event.At.Before(cutoff)) {
			continue
		}
		record := scores[event.LocationID]
		if record == nil {
			continue
		}
		switch event.Kind {
		case View:
			record.Views++
			if unique[event.LocationID] == nil {
				unique[event.LocationID] = map[int64]struct{}{}
			}
			unique[event.LocationID][event.UserID] = struct{}{}
		case FavoriteAdded:
			record.Additions++
		case FavoriteRemoved:
			record.Removals++
		}
	}
	for id, seen := range unique {
		scores[id].UniqueViews = len(seen)
	}
	for id, dates := range leads {
		r := scores[id]
		if r == nil {
			continue
		}
		for _, d := range dates {
			if !d.After(now) && (cutoff.IsZero() || !d.Before(cutoff)) {
				r.Leads++
			}
		}
	}
	output := make([]Count, 0, len(scores))
	for _, r := range scores {
		output = append(output, *r)
	}
	score := func(r Count) int {
		switch kind {
		case "views":
			return r.Views
		case "unique":
			return r.UniqueViews
		case "added":
			return r.Additions
		case "saved":
			return r.CurrentFavorites
		case "leads":
			return r.Leads
		default:
			return r.Views
		}
	}
	sort.Slice(output, func(i, j int) bool {
		a, b := score(output[i]), score(output[j])
		if a != b {
			return a > b
		}
		return strings.Compare(output[i].LocationID, output[j].LocationID) < 0
	})
	if len(output) > 10 {
		return output[:10]
	}
	return output
}
func (s *Store) Reload() error {
	n, err := Open(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.events = n.events
	s.mu.Unlock()
	return nil
}
func (s *Store) Path() string { return s.path }
