package catalog

import (
 "os"
 "path/filepath"
 "strings"
 "testing"
)
func TestPurgeLocationErasesCustomDataAndSurvivesRestart(t *testing.T) {
 root:=t.TempDir()
 seed:=filepath.Join(root,"seed.json")
 if err:=os.WriteFile(seed,[]byte(`[{"id":"020","title":"Базовая"},{"id":"021","title":"Другая"}]`),0600);err!=nil{t.Fatal(err)}
 s,err:=New(root,seed,filepath.Join(root,"overrides.json"));if err!=nil{t.Fatal(err)}
 id:="custom-1122334455667788"
 if err:=s.CreateAdminLocation(Location{ID:id,Title:"Секретная карточка",Description:"Удаляемые данные"});err!=nil{t.Fatal(err)}
 if err:=s.AdminEdit(id,"",CardRevision{Title:"Секретная карточка",Description:"Секрет",Sections:[]CardSection{}});err!=nil{t.Fatal(err)}
 if err:=s.PurgeLocation(id);err!=nil{t.Fatal(err)}
 for _,path:=range []string{s.AdminLocationsPath(),s.AdminCardsPath()} {
  raw,err:=os.ReadFile(path);if err!=nil{t.Fatal(err)}
  if strings.Contains(string(raw),id)||strings.Contains(string(raw),"Секрет"){t.Fatalf("deleted content survived in %s: %s",path,raw)}
 }
 if err:=s.PurgeLocation(id);err==nil{t.Fatal("duplicate purge accepted")}
 fresh,err:=New(root,seed,filepath.Join(root,"overrides.json"));if err!=nil{t.Fatal(err)}
 if _,ok:=fresh.Find(id);ok{t.Fatal("custom location resurrected")}
 if err:=fresh.PurgeLocation("020");err!=nil{t.Fatal(err)}
 fresh2,err:=New(root,seed,filepath.Join(root,"overrides.json"));if err!=nil{t.Fatal(err)}
 if _,ok:=fresh2.Find("020");ok{t.Fatal("curated location resurrected from seed")}
 if _,ok:=fresh2.Find("021");!ok{t.Fatal("unrelated location removed")}
 tombstones,err:=ReadDeletedLocations(fresh2.DeletedLocationsPath());if err!=nil{t.Fatal(err)}
 if !tombstones[id] || !tombstones["020"]{t.Fatal("missing permanent tombstones")}
}
