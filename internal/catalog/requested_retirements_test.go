package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeWithdrawnLocationsStore(t *testing.T, root string) *Store {
	t.Helper()
	seed := filepath.Join(root, "seed.json")
	if err := os.WriteFile(seed, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewWithBundled(root, seed, filepath.Join(root, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestWithdrawnLocationsAbsentFromClientCatalogAndSearch(t *testing.T) {
	root := t.TempDir()
	s := makeWithdrawnLocationsStore(t, root)
	hidden := []string{"005", "008", "009", "084", "087", "114"}
	if len(s.List()) != 114-len(hidden) {
		t.Fatalf("expected %d curated locations, got %d", 114-len(hidden), len(s.List()))
	}
	for _, id := range hidden {
		if _, exists := s.Find(id); exists {
			t.Errorf("withdrawn location %s still visible", id)
		}
	}
	for _, name := range []string{"Лебедянская", "Кавказский бульвар", "Бирюлёво", "Гражданская", "Сапроново"} {
		if results := Search(s.List(), name); len(results) != 0 {
			t.Errorf("withdrawn location %q returned in search: %+v", name, results)
		}
	}
	for _, id := range []string{"023", "085"} {
		if _, exists := s.Find(id); !exists {
			t.Errorf("unrelated Красногорск location %s was hidden", id)
		}
	}
	deleted, err := ReadDeletedLocations(s.DeletedLocationsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range hidden {
		if !deleted[id] {
			t.Errorf("withdrawn ID %s absent from reversible deletion ledger", id)
		}
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, id := range hidden {
		if _, exists := s.Find(id); exists {
			t.Errorf("withdrawn ID %s reappeared after reload", id)
		}
	}
}

func TestWithdrawnLocationsRespectLegacyDeletionsAndAdminRestore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, DeletedLocationsFilename), []byte(`{"002":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := makeWithdrawnLocationsStore(t, root)
	if _, found := s.Find("002"); found {
		t.Fatal("existing admin deletion unexpectedly restored")
	}
	deleted, err := ReadDeletedLocations(s.DeletedLocationsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 7 {
		t.Fatalf("expected original removal plus six requested removals; got %+v", deleted)
	}
	if err := s.SetDeletedLocation("084", false); err != nil {
		t.Fatal(err)
	}
	if loc, found := s.Find("084"); !found || !strings.EqualFold(loc.Title, "Красногорск") {
		t.Fatalf("admin restore did not recover the exact original location: %+v found=%v", loc, found)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, found := s.Find("084"); !found {
		t.Fatal("one-time migration re-deleted admin-restored location")
	}
	if _, found := s.Find("005"); found {
		t.Fatal("other requested removals not retained")
	}
	s2 := makeWithdrawnLocationsStore(t, root)
	if _, found := s2.Find("084"); !found {
		t.Fatal("restored location lost after restart")
	}
	if _, found := s2.Find("002"); found {
		t.Fatal("legacy deletion lost after restart")
	}
}
