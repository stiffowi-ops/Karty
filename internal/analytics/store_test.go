package analytics

import (
	"path/filepath"
	"testing"
	"time"
)

func TestViewsAndFavoritesPersistAndRank(t *testing.T) {
	path := filepath.Join(t.TempDir(), "analytics.jsonl")
	s, _ := Open(path)
	now := time.Now().UTC()
	for _, e := range []Event{{now, 11, "001", View}, {now, 11, "001", View}, {now, 12, "001", View}, {now, 11, "002", View}, {now, 11, "002", FavoriteAdded}, {now, 12, "002", FavoriteAdded}, {now.Add(-40 * 24 * time.Hour), 13, "001", FavoriteAdded}} {
		if err := s.Record(e.UserID, e.LocationID, e.Kind, e.At); err != nil {
			t.Fatal(err)
		}
	}
	stats := s.Rankings(now, 30, "views", []string{"001", "002"}, map[string]int{"001": 1, "002": 2}, nil)
	if stats[0].LocationID != "001" || stats[0].Views != 2 || stats[0].UniqueViews != 2 {
		t.Fatalf("unexpected views: %+v", stats)
	}
	stats = s.Rankings(now, 30, "added", []string{"001", "002"}, nil, nil)
	if stats[0].LocationID != "002" || stats[0].Additions != 2 {
		t.Fatalf("unexpected favorites: %+v", stats)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Snapshot()) != 6 {
		t.Fatalf("events not persisted: %d", len(s2.Snapshot()))
	}
}
