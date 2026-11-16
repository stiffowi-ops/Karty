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

func TestPartnerCardOverrideReload(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	ov := filepath.Join(root, "overrides.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"002","title":"Метро Кленовый бульвар","filename":"002.png","image_ext":".png"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ov, []byte(`{"002":{"line_status":"Открыто","landmarks":"Нагатино-Садовники","traffic":"Поток","payers":"Жители","opportunity":"Возможность","conclusion":"Итог"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(filepath.Join(root, "runtime"), seed, ov)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := store.Find("002")
	if !ok || l.LineStatus != "Открыто" || l.Traffic != "Поток" || l.Payers != "Жители" || l.Opportunity != "Возможность" || l.Conclusion != "Итог" || l.Landmarks != "Нагатино-Садовники" {
		t.Fatalf("fields missing from overrides: %+v", l)
	}
}

func TestPartnerDefaultsPreserveUserCustomization(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	override := filepath.Join(root, "overrides.json")
	partner := filepath.Join(root, "partner_cards.json")
	mustWrite := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(seed, `[{"id":"002","title":"Метро Кленовый бульвар","filename":"002.png","image_ext":".png"},{"id":"003","title":"Метро Курьяново","landmarks":"Старый проверенный ориентир","filename":"003.png","image_ext":".png"}]`)
	mustWrite(partner, `{"002":{"line_status":"Линия · открыто","traffic":"Трафик из шаблона"},"003":{}}`)
	mustWrite(override, `{"002":{"lat":55.6,"lon":37.3,"payers":"Локальная правка"}}`)
	store, err := New(filepath.Join(root, "runtime"), seed, override)
	if err != nil {
		t.Fatal(err)
	}
	one, ok := store.Find("002")
	if !ok || one.Traffic != "Трафик из шаблона" || one.Payers != "Локальная правка" || one.Latitude == nil || *one.Latitude != 55.6 || one.Longitude == nil || *one.Longitude != 37.3 {
		t.Fatalf("merge precedence wrong: %+v", one)
	}
	two, _ := store.Find("003")
	if two.Landmarks != "Старый проверенный ориентир" {
		t.Fatalf("empty template erased existing data: %+v", two)
	}
	if err := store.SaveBase(store.List()); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	again, _ := store.Find("002")
	if again.Traffic != "Трафик из шаблона" || again.Payers != "Локальная правка" {
		t.Fatalf("data lost on reload: %+v", again)
	}
}

func TestCuratedCatalogHas114PointsWithDescriptions(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	ov := filepath.Join(root, "overrides.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"001","title":"Метро Остров Мечты"}]`), 0600); err != nil { t.Fatal(err) }
	store, err := NewWithBundled(filepath.Join(root, "runtime"), seed, ov)
	if err != nil { t.Fatal(err) }
	if got := len(store.List()); got != 114 { t.Fatalf("want 114 points, got %d", got) }
	for _, item := range store.List() {
		if item.LineStatus == "" || item.Traffic == "" || item.Payers == "" || item.Opportunity == "" || item.Conclusion == "" {
			t.Fatalf("incomplete partner card for %q (%s)", item.Title, item.ID)
		}
	}
	if _, ok := store.Find("114"); !ok { t.Fatal("last curated point missing") }
	if err := os.WriteFile(ov, []byte(`{"114":{"traffic":"Моя правка"}}`), 0600); err != nil { t.Fatal(err) }
	if err := store.Reload(); err != nil { t.Fatal(err) }
	item, _ := store.Find("114")
	if item.Traffic != "Моя правка" { t.Fatalf("user override lost: %q", item.Traffic) }
}
