package adminbackup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"locationbot/internal/catalog"
)

func TestBackupCustomLocationRestoresToOlderRuntime(t *testing.T) {
	root := t.TempDir()
	custom := catalog.Location{ID: "custom-123abc", Title: "Метро новая локация", Sections: []catalog.CardSection{{ID: "traffic", Title: "Трафик", Body: "Возможности", Enabled: true}}}
	newEntries := map[string]catalog.Location{custom.ID: custom}
	raw, _ := json.Marshal(newEntries)
	if err := os.WriteFile(filepath.Join(root, catalog.AdminLocationsFilename), raw, 0600); err != nil {
		t.Fatal(err)
	}
	locs := append(sample(), custom)
	zipRaw, err := Build(root, locs)
	if err != nil {
		t.Fatal(err)
	}
	arc, err := Examine(zipRaw)
	if err != nil {
		t.Fatal(err)
	}
	if arc.Inspect.Locations != 2 {
		t.Fatalf("custom location missing from backup metadata: %+v", arc.Inspect)
	}
	if _, found := arc.Contents[catalog.AdminLocationsFilename]; !found {
		t.Fatal("custom locations not included in backup")
	}
	if err := os.Remove(filepath.Join(root, catalog.AdminLocationsFilename)); err != nil {
		t.Fatal(err)
	}
	// Restore into the older catalog, which has only the curated locations.
	if _, err := Restore(root, arc, "cards", sample()); err != nil {
		t.Fatal(err)
	}
	restored, err := catalog.ReadAdminLocations(filepath.Join(root, catalog.AdminLocationsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[custom.ID].Title != custom.Title {
		t.Fatalf("custom location was not restored: %+v", restored)
	}
	saved, _ := os.ReadFile(filepath.Join(root, "admin_cards.json"))
	var revisions map[string]catalog.CardRevision
	if err := json.Unmarshal(saved, &revisions); err != nil {
		t.Fatal(err)
	}
	if revisions[custom.ID].Title != custom.Title {
		t.Fatalf("custom revision was not restored: %+v", revisions)
	}
}

func TestOldBackupPreservesLaterAdminLocation(t *testing.T) {
	root := t.TempDir()
	zipRaw, err := Build(root, sample())
	if err != nil {
		t.Fatal(err)
	}
	old, err := Examine(zipRaw)
	if err != nil {
		t.Fatal(err)
	}
	id := "custom-deadbeef"
	custom := catalog.Location{ID: id, Title: "Метро новая", Sections: []catalog.CardSection{}}
	raw, _ := json.Marshal(map[string]catalog.Location{id: custom})
	if err := os.WriteFile(filepath.Join(root, catalog.AdminLocationsFilename), raw, 0600); err != nil {
		t.Fatal(err)
	}
	live := append(sample(), custom)
	if _, err := Restore(root, old, "cards", live); err != nil {
		t.Fatal(err)
	}
	saved, err := catalog.ReadAdminLocations(filepath.Join(root, catalog.AdminLocationsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := saved[id]; !ok || got.Title != custom.Title {
		t.Fatalf("new custom location lost after restoring old backup: %+v", saved)
	}
	if _, err := Restore(root, old, "full", live); err != nil {
		t.Fatal(err)
	}
	saved, err = catalog.ReadAdminLocations(filepath.Join(root, catalog.AdminLocationsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := saved[id]; !ok {
		t.Fatal("full restore lost more recent custom location")
	}
}

func TestRejectCustomLocationArchiveMissingRuntimeFile(t *testing.T) {
	root := t.TempDir()
	extra := catalog.Location{ID: "custom-9876", Title: "Локация без сохранённого источника", Sections: []catalog.CardSection{}}
	zipRaw, err := Build(root, append(sample(), extra))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Examine(zipRaw); err == nil {
		t.Fatal("accepted inconsistent backup containing a custom location without runtime data")
	}
}
