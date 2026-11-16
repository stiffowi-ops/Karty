package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearchRussianYo(t *testing.T) {
	loc := []Location{{ID: "001", Title: "Метро Королёв"}, {ID: "002", Title: "Метро Троицк"}}
	got := Search(loc, "КОРОЛЕВ")
	if len(got) != 1 || got[0].ID != "001" {
		t.Fatalf("unexpected result: %+v", got)
	}
	got = Search(loc, "метро троицк")
	if len(got) != 1 || got[0].ID != "002" {
		t.Fatalf("unexpected result: %+v", got)
	}
}
func TestReloadAndOverrides(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	ov := filepath.Join(root, "overrides.json")
	if e := os.WriteFile(seed, []byte(`[{"id":"001","title":"Metro","filename":"001 Metro.png","category":"Метро","image_ext":".png"}]`), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(ov, []byte(`{"001":{"title":"Новое название","lat":55.5,"lon":37.6}}`), 0644); e != nil {
		t.Fatal(e)
	}
	store, e := New(filepath.Join(root, "runtime"), seed, ov)
	if e != nil {
		t.Fatal(e)
	}
	loc, ok := store.Find("001")
	if !ok || loc.Title != "Новое название" || loc.Latitude == nil || *loc.Latitude != 55.5 {
		t.Fatalf("wrong override: %+v", loc)
	}
	if e = store.SaveBase([]Location{{ID: "002", Title: "Другая", Filename: "x.png", ImageExt: ".png"}}); e != nil {
		t.Fatal(e)
	}
	if e = store.Reload(); e != nil {
		t.Fatal(e)
	}
	if len(store.List()) != 1 || store.List()[0].ID != "002" {
		t.Fatal("base not reloaded")
	}
}
func TestValidIDs(t *testing.T) {
	for _, id := range []string{"../../etc/passwd", "", "a/b", "АБВ"} {
		if ValidID(id) {
			t.Fatalf("unsafe ID permitted: %q", id)
		}
	}
	for _, id := range []string{"001", "file-ab1925", "abc"} {
		if !ValidID(id) {
			t.Fatalf("valid ID rejected: %q", id)
		}
	}
}
func TestRatingOverride(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	ov := filepath.Join(root, "overrides.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"001","title":"Station","filename":"001 Station.png","image_ext":".png"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ov, []byte(`{"001":{"rating":"8/10","description":"Перспективная точка"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(filepath.Join(root, "runtime"), seed, ov)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := s.Find("001")
	if !ok || l.Rating != "8/10" || l.Description != "Перспективная точка" {
		t.Fatalf("wrong override: %+v", l)
	}
}
