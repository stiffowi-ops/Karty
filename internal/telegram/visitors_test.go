package telegram

import (
 "context"
 "net/http"
 "path/filepath"
 "strings"
 "testing"
 "time"
)

func TestVisitorFirstRouteAndRepeatVisits(t *testing.T){
 path:=filepath.Join(t.TempDir(),"visitors.json")
 store,err:=openVisitorStore(path);if err!=nil{t.Fatal(err)}
 u:=user{ID:77,FirstName:"Иван",LastName:"Тестовый",Username:"sampleuser"}
 now:=time.Date(2026,11,17,8,0,0,0,time.UTC)
 for _,tc:=range []struct{after time.Duration;action string;start bool}{
  {0,"Главное меню",true},
  {time.Minute,"Список локаций",false},
  {2*time.Minute,"Карточка: Троицк",false},
  {3*time.Minute,"Главное меню",true},
  {8*time.Minute,"Главное меню",true},
  {9*time.Minute,"Избранное",false},
  {40*time.Minute,"Поиск локации",false},
 }{if err:=store.record(u,tc.action,tc.start,now.Add(tc.after));err!=nil{t.Fatal(err)}}
 p,ok:=store.find(77)
 if !ok{t.Fatal("visitor missing")}
 if p.Daily["2026-11-17"]!=3 {t.Fatalf("daily visit count should be 3, got %+v",p.Daily)}
 if !p.FirstRouteClosed || len(p.FirstRoute)!=4 {t.Fatalf("first route not preserved: %+v",p.FirstRoute)}
 if p.FirstRoute[2].Name!="Карточка: Троицк" {t.Fatalf("first route changed: %+v",p.FirstRoute)}
 loaded,err:=openVisitorStore(path);if err!=nil{t.Fatal(err)}
 q,_:=loaded.find(77)
 if q.Daily["2026-11-17"]!=3 || len(q.FirstRoute)!=4 {t.Fatalf("lost sessions after restart: %+v",q)}
 if !strings.Contains(visitorHTML(q,now.Add(40*time.Minute)),"Иван") || !strings.Contains(visitorHTML(q,now.Add(40*time.Minute)),"Посещений сегодня: <b>3</b>"){t.Fatal("visitor summary missing")}
 if !strings.Contains(visitorProfileURL(q),"https://t.me/sampleuser"){t.Fatal("no profile link")}
}

func TestVisitorAlertOnceAndLaterLeadStatus(t *testing.T){
 b,tr:=makeLeadTestBot(t)
 visitors,err:=openVisitorStore(filepath.Join(t.TempDir(),"visitors.json"));if err!=nil{t.Fatal(err)}
 b.visitors=visitors
 u:=user{ID:88,FirstName:"Ольга",Username:"client_88"}
 at:=time.Now().UTC().Add(-2*time.Hour)
 if err:=visitors.record(u,"Главное меню",true,at);err!=nil{t.Fatal(err)}
 b.sendVisitorAlerts(context.Background(),at.Add(59*time.Minute))
 if len(tr.requests)!=0{t.Fatalf("premature alert: %v",tr.requests)}
 b.sendVisitorAlerts(context.Background(),at.Add(61*time.Minute))
 if len(tr.requests)!=2{t.Fatalf("exactly two admin alerts expected: %v",tr.requests)}
 b.sendVisitorAlerts(context.Background(),at.Add(5*time.Hour))
 if len(tr.requests)!=2{t.Fatalf("repeat alerts sent: %v",tr.requests)}
 p,_:=visitors.find(88)
 if !p.Alerted || len(p.AlertRecipients)!=2 {t.Fatalf("alert delivery not persisted: %+v",p)}
 // The same card updates as soon as a lead exists, with no new alarm.
 if err:=visitors.markLead(88);err!=nil{t.Fatal(err)}
 p,_=visitors.find(88)
 if !p.HasLead || !strings.Contains(visitorHTML(p,time.Now()),"Заявка получена"){t.Fatal("later application not reflected")}
 if err:=visitors.record(u,"Главное меню",true,time.Now());err!=nil{t.Fatal(err)}
 b.sendVisitorAlerts(context.Background(),time.Now().Add(2*time.Hour))
 if len(tr.requests)!=2{t.Fatalf("new alert after submitted lead: %v",tr.requests)}
}

func TestVisitorPersistedNoAlertOnExistingApplication(t *testing.T){
 b,tr:=makeLeadTestBot(t)
 visitors,err:=openVisitorStore(filepath.Join(t.TempDir(),"visitors.json"));if err!=nil{t.Fatal(err)}
 b.visitors=visitors
 draft:=leadDraft{Kind:"existing",Name:"Иван",Phone:"+79990000000",LocationID:"001",Stage:"review",Source:"menu"}
 if err:=b.leads.putDraft(100,draft);err!=nil{t.Fatal(err)}
 if _,already,err:=b.leads.submit(100,"user","Троицк");err!=nil||already{t.Fatalf("submit: %v %v",err,already)}
 if err:=visitors.record(user{ID:100,FirstName:"Иван"},"Главное меню",true,time.Now().Add(-2*time.Hour));err!=nil{t.Fatal(err)}
 b.sendVisitorAlerts(context.Background(),time.Now())
 if len(tr.requests)!=0{t.Fatalf("alarm for existing applicant: %v",tr.requests)}
 p,_:=visitors.find(100)
 if !p.HasLead{t.Fatal("existing application state not propagated")}
}

func TestVisitorPrivateAdminOnlyAndNoFreeText(t *testing.T) {
 b,_:=makeLeadTestBot(t)
 visitors,err:=openVisitorStore(filepath.Join(t.TempDir(),"visitors.json"));if err!=nil{t.Fatal(err)}
 b.visitors=visitors
 b.observeVisitor(user{ID:200,FirstName:"Admin"},"Главное меню",true)
 if len(visitors.snapshot())!=0{t.Fatal("admins must not enter tracking")}
 b.observeVisitor(user{ID:100,FirstName:"Client"},"Поиск локации",true)
 p,_:=visitors.find(100)
 if p.UserID!=100{t.Fatal("client not observed")}
 tr:=&captureTransport{}
 b.http=&http.Client{Transport:tr}
 if err:=b.handleAdminCallback(context.Background(),100,user{ID:100},"adm:visitors");err!=nil{t.Fatal(err)}
 if len(tr.requests)!=0{t.Fatal("non-admin accessed visitor dashboard")}
}
