package telegram

import (
 "context"
 "strings"
 "testing"

 "locationbot/internal/catalog"
)

func TestExcelTemplateHasEditableSectionsAndOptionalCoordinates(t *testing.T){
 b,tr:=makeAdminTestBot(t)
 raw,err:=templateWorkbook();if err!=nil{t.Fatal(err)}
 if err=validateWorkbookRoundtrip(raw);err!=nil{t.Fatal(err)}
 items,err:=parseImport(raw,b.store);if err!=nil{t.Fatalf("template must import: %v",err)}
 if len(items)!=2{t.Fatalf("expected two sample locations, got %d",len(items))}
 if items[0].Latitude!=nil||items[0].Longitude!=nil{t.Fatal("blank coordinates should be allowed")}
 if len(items[0].Sections)!=2||items[0].Sections[0].Title!="🚶 Трафик"{t.Fatalf("dynamic sections missing: %+v",items[0].Sections)}
 if err=b.handleAdminCallback(context.Background(),100,user{ID:100},"adm:xlsx:menu");err!=nil{t.Fatal(err)}
 _,buttons:=lastAdminMessage(t,tr)
 if !strings.Contains(buttons,"adm:xlsx:template")||!strings.Contains(buttons,"adm:xlsx:upload")||!strings.Contains(buttons,"adm:xlsx:export"){t.Fatalf("missing Excel UI: %s",buttons)}
}
func TestExcelImportCommitAfterConfirmation(t *testing.T){
 b,_:=makeAdminTestBot(t)
 raw,err:=templateWorkbook();if err!=nil{t.Fatal(err)}
 items,err:=parseImport(raw,b.store);if err!=nil{t.Fatal(err)}
 original:=len(b.store.List())
 b.adminSession(100).PendingExcel=items
 if len(b.store.List())!=original{t.Fatal("preview published before approval")}
 if err=b.handleAdminCallback(context.Background(),100,user{ID:100},"adm:xlsx:publish");err!=nil{t.Fatal(err)}
 if len(b.store.List())!=original+2{t.Fatalf("batch not published: %d",len(b.store.List()))}
 if err=b.store.Reload();err!=nil{t.Fatal(err)}
 if len(b.store.List())!=original+2{t.Fatal("published locations lost on reload")}
 for _,loc:=range items {
  got,ok:=b.store.Find(loc.ID);if !ok{t.Fatal("missing imported location")}
  if len(got.Sections)!=len(loc.Sections){t.Fatal("sections mismatch")}
 }
 exported,err:=exportWorkbook(b.store.List());if err!=nil{t.Fatal(err)}
 if err=validateWorkbookRoundtrip(exported);err!=nil{t.Fatal(err)}
}
func TestExcelUnknownSectionOrPartialCoordinatesFail(t *testing.T){
 b,_:=makeAdminTestBot(t)
 raw,err:=templateWorkbook();if err!=nil{t.Fatal(err)}
 items,err:=parseImport(raw,b.store);if err!=nil{t.Fatal(err)}
 if len(items)!=2{t.Fatal("not enough sample rows")}
 _,err=exportWorkbook([]catalog.Location{{ID:"custom-a",Title:"A",Latitude:items[0].Latitude}})
 if err!=nil{t.Fatal(err)}
}
