package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminLocationCreationSurvivesSyncAndReload(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"020","title":"Исходная","filename":"020.png"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(root, seed, filepath.Join(root, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewAdminLocationID()
	if err != nil {
		t.Fatal(err)
	}
	if !IsAdminLocationID(id) {
		t.Fatalf("invalid generated id %s", id)
	}
	loc := Location{ID: id, Title: "Метро Новая", Description: "Первое описание", Sections: []CardSection{{ID: "retail", Title: "🏪 Форматы бизнеса", Body: "Текст", Enabled: true}}}
	if _, found := s.Find(id); found {
		t.Fatal("draft already public")
	}
	if err = s.CreateAdminLocation(loc); err != nil {
		t.Fatal(err)
	}
	if _, found := s.Find(id); !found {
		t.Fatal("new location not visible after publish")
	}
	if err = s.CreateAdminLocation(loc); err == nil {
		t.Fatal("duplicate admin location accepted")
	}
	// Saving the synchronized base, with no custom ID, must not delete admin locations.
	if err = s.SaveBase([]Location{{ID: "020", Title: "Исходная обновлена"}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Reload(); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Find(id)
	if !ok || got.Title != "Метро Новая" || len(got.Sections) != 1 {
		t.Fatalf("custom location lost after sync/reload: %+v", got)
	}
	s2, err := New(root, seed, filepath.Join(root, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok = s2.Find(id); !ok {
		t.Fatal("location did not survive store restart")
	}
	if strings.TrimSpace(got.ImageURL) != "" {
		t.Fatal("unexpected user-controlled image URL")
	}
}

func TestAdminLocationValidationDoesNotModifyLiveCatalog(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"020","title":"Исходная"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(root, seed, filepath.Join(root, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Location{{ID: "020", Title: "Duplicate"}, {ID: "custom-A", Title: "Invalid"}, {ID: "custom-ab", Title: ""}} {
		if err := s.CreateAdminLocation(bad); err == nil {
			t.Fatalf("bad location accepted %+v", bad)
		}
	}
	if len(s.List()) != 1 {
		t.Fatalf("invalid creation modified catalog: %+v", s.List())
	}
	if _, err := os.Stat(s.AdminLocationsPath()); !os.IsNotExist(err) {
		t.Fatalf("unexpected runtime file for invalid drafts: %v", err)
	}
}

func TestAdminLocationDeletionPersistsAndCanBeRestored(t *testing.T) {
	root:=t.TempDir()
	seed:=filepath.Join(root,"seed.json")
	if err:=os.WriteFile(seed,[]byte(`[{"id":"020","title":"Базовая"},{"id":"021","title":"Вторая"}]`),0600);err!=nil{t.Fatal(err)}
	s,err:=New(root,seed,filepath.Join(root,"overrides.json"))
	if err!=nil{t.Fatal(err)}
	id,err:=NewAdminLocationID()
	if err!=nil{t.Fatal(err)}
	if err=s.CreateAdminLocation(Location{ID:id,Title:"Добавленная"});err!=nil{t.Fatal(err)}
	for _,removed:=range []string{"020",id} {
		if err=s.SetDeletedLocation(removed,true);err!=nil{t.Fatal(err)}
		if _,ok:=s.Find(removed);ok{t.Fatalf("deleted location %s is public",removed)}
	}
	if err=s.Reload();err!=nil{t.Fatal(err)}
	restarted,err:=New(root,seed,filepath.Join(root,"overrides.json"))
	if err!=nil{t.Fatal(err)}
	if len(restarted.List())!=1{t.Fatalf("expected only 021, got %+v",restarted.List())}
	if err=restarted.SetDeletedLocation("020",true);err==nil{t.Fatal("double deletion accepted")}
	if err=restarted.SetDeletedLocation("020",false);err!=nil{t.Fatal(err)}
	if err=restarted.SetDeletedLocation(id,false);err!=nil{t.Fatal(err)}
	if _,ok:=restarted.Find(id);!ok{t.Fatal("restored custom location lost")}
	if _,ok:=restarted.Find("020");!ok{t.Fatal("restored base location lost")}
	if err=restarted.SetDeletedLocation("021",false);err==nil{t.Fatal("restored a never-deleted location")}
}
