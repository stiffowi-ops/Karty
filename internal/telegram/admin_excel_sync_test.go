package telegram

import (
 "strings"
 "testing"

 "locationbot/internal/xlsx"
 "locationbot/internal/catalog"
)

func fullSyncTestSheets(t *testing.T, data []byte)[]xlsx.Sheet{
 t.Helper()
 names,err:=xlsx.SheetNames(data);if err!=nil{t.Fatal(err)}
 content,err:=xlsx.Read(data,names...);if err!=nil{t.Fatal(err)}
 out:=make([]xlsx.Sheet,0,len(names))
 for _,name:=range names{out=append(out,xlsx.Sheet{Name:name,Rows:content[name]})}
 return out
}
func TestFullCatalogSheetRoundtripAndCustomSections(t *testing.T){
 b,_:=makeAdminTestBot(t)
 if err:=b.store.CreateAdminLocation(catalog.Location{ID:"custom-aabbccddeeff1122",Title:"Тестовая дополнительная точка",Sections:[]catalog.CardSection{}});err!=nil{t.Fatal(err)}
 initial:=b.store.List()
 raw,err:=exportFullCatalog(b.store);if err!=nil{t.Fatal(err)}
 sheets:=fullSyncTestSheets(t,raw)
 if len(sheets)!=len(initial)+1{t.Fatalf("expected registry+locations, got %d",len(sheets))}
 if sheets[0].Name!=fullSyncRegistry{t.Fatal("registry must be first")}
 preview,err:=parseFullCatalog(raw,b.store);if err!=nil{t.Fatal(err)}
 if len(preview.Desired)!=len(initial)||len(preview.Removals)!=0||preview.Updated!=0||preview.Added!=0{
  t.Fatalf("roundtrip changed catalog: %+v",preview)
 }
 first:=sheets[1].Rows
 first[2][1]="Совершенно новое название в каталоге"
 first[16]=[]string{"1","🏪 Особый раздел","Кастомный текст","Да"}
 // Remove one existing sheet from the workbook and create one new sheet.
 removedID:=sheets[2].Rows[1][1]
 sheets=append(sheets[:2],sheets[3:]...)
 newSheet:=locationSheet(preview.Desired[0])
 newSheet=newSheet[:16]
 newSheet[1][1]=""
 newSheet[2][1]="Новая точка синхронизации 2026"
 newSheet=append(newSheet,[]string{"1","📦 Индивидуальная категория","Новое содержание","Нет"})
 sheets=append(sheets,xlsx.Sheet{Name:"НОВАЯ_ТОЧКА",Rows:newSheet})
 modified,err:=xlsx.Write(sheets);if err!=nil{t.Fatal(err)}
 result,err:=parseFullCatalog(modified,b.store);if err!=nil{t.Fatal(err)}
 if result.Added!=1||result.Updated<1||len(result.Removals)!=1||result.Removals[0]!=removedID{
  t.Fatalf("unexpected diff: add=%d update=%d remove=%v",result.Added,result.Updated,result.Removals)
 }
 if err=b.store.ApplyCatalogSync(result.Desired,result.Removals,result.Fingerprint);err!=nil{t.Fatal(err)}
 if len(b.store.List())!=len(initial){t.Fatalf("expected replacement to preserve total count: %d",len(b.store.List()))}
 if _,ok:=b.store.Find(removedID);ok{t.Fatal("deleted station still visible")}
 if err=b.store.Reload();err!=nil{t.Fatal(err)}
 if _,ok:=b.store.Find(removedID);ok{t.Fatal("deleted station came back on reload")}
 var foundNew,foundEdited bool
 for _,loc:=range b.store.List(){
  if loc.Title=="Новая точка синхронизации 2026"{
   foundNew=true
   if len(loc.Sections)!=1||loc.Sections[0].Enabled{t.Fatal("custom section lost")}
  }
  if loc.Title=="Совершенно новое название в каталоге"{foundEdited=true}
 }
 if !foundNew||!foundEdited{t.Fatal("updated or added entry absent")}
}
func TestFullCatalogRejectsStaleAndBadSection(t *testing.T){
 b,_:=makeAdminTestBot(t)
 original,err:=exportFullCatalog(b.store);if err!=nil{t.Fatal(err)}
 sheets:=fullSyncTestSheets(t,original)
 sheets[1].Rows[16]=[]string{"16","Неверный раздел","Текст","Да"}
 invalid,err:=xlsx.Write(sheets);if err!=nil{t.Fatal(err)}
 if _,err=parseFullCatalog(invalid,b.store);err==nil||!strings.Contains(err.Error(),"порядок"){t.Fatalf("invalid section accepted: %v",err)}
 items:=b.store.List()
 if err=b.store.PurgeLocation(items[0].ID);err!=nil{t.Fatal(err)}
 if _,err=parseFullCatalog(original,b.store);err==nil||!strings.Contains(err.Error(),"изменился"){t.Fatalf("stale catalog accepted: %v",err)}
}
func TestFullCatalogRequiresOriginalExportAndRejectsForeignID(t *testing.T){
 b,_:=makeAdminTestBot(t)
 raw,err:=templateWorkbook();if err!=nil{t.Fatal(err)}
 if _,err=parseFullCatalog(raw,b.store);err==nil{t.Fatal("legacy import template treated as complete catalog")}
 original,err:=exportFullCatalog(b.store);if err!=nil{t.Fatal(err)}
 sheets:=fullSyncTestSheets(t,original)
 sheets[1].Rows[1][1]="custom-unknown1234"
 modified,err:=xlsx.Write(sheets);if err!=nil{t.Fatal(err)}
 if _,err=parseFullCatalog(modified,b.store);err==nil{t.Fatal("unknown existing ID accepted")}
}
