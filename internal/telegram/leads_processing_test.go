package telegram

import (
 "context"
 "encoding/json"
 "path/filepath"
 "strings"
 "sync"
 "testing"
)

func TestLeadTakenInWorkAllowsSecondApplicationAndKeepsHistory(t *testing.T) {
 b,_:=makeLeadTestBot(t)
 firstDraft:=leadDraft{Kind:"existing",Name:"Александр",Phone:"+79998887766",LocationID:"001",Stage:"review",Source:"card"}
 if err:=b.leads.putDraft(100,firstDraft);err!=nil{t.Fatal(err)}
 first,already,err:=b.leads.submit(100,"client","Метро Троицк")
 if err!=nil||already||first.Number!=1{t.Fatalf("first application: %+v %v %v",first,already,err)}
 if err:=b.leads.putDraft(100,firstDraft);err==nil{t.Fatal("unprocessed application must block new drafts")}
 var wg sync.WaitGroup
 var mu sync.Mutex
 claimed:=0
 for _,id:=range []int64{200,300}{
  wg.Add(1)
  go func(adminID int64){
   defer wg.Done()
   _,changed,e:=b.leads.takeInWork(1,adminID)
   if e!=nil{t.Error(e);return}
   if changed{mu.Lock();claimed++;mu.Unlock()}
  }(id)
 }
 wg.Wait()
 if claimed!=1{t.Fatalf("expected exactly one claim, got %d",claimed)}
 processed,ok:=b.leads.submitted(100)
 if !ok||!processed.TakenInWork||processed.TakenBy<=0||processed.TakenAt==nil {t.Fatalf("first application not processed %+v",processed)}
 if err:=b.leads.putDraft(100,firstDraft);err!=nil{t.Fatalf("processed lead must allow new draft: %v",err)}
 second,already,err:=b.leads.submit(100,"client","Метро Троицк")
 if err!=nil||already||second.Number!=2||second.TakenInWork {t.Fatalf("new application %+v %v %v",second,already,err)}
 if err:=b.leads.putDraft(100,firstDraft);err==nil{t.Fatal("new pending application must block third application")}
 records:=b.leads.allSubmitted()
 if len(records)!=2||records[0].Number!=2||records[1].Number!=1||!records[1].TakenInWork {t.Fatalf("history lost: %+v",records)}
 if err:=b.leads.markDelivered(100,1,200,123);err!=nil{t.Fatal(err)}
 old,ok:=b.leads.byNumber(1)
 if !ok||old.AdminMessages["200"]!=123 {t.Fatalf("previous notification message not tracked: %+v",old)}
 reloaded,err:=openLeadStore(filepath.Join(b.store.DataDir(),"leads.json"))
 if err!=nil{t.Fatal(err)}
 if len(reloaded.allSubmitted())!=2 {t.Fatal("history lost after restart")}
 pending,_:=reloaded.submitted(100)
 if pending.Number!=2||pending.TakenInWork {t.Fatalf("new pending application no longer blocks: %+v",pending)}
}

func TestLeadAdminButtonAndCallbackAuthorization(t *testing.T){
 b,tr:=makeLeadTestBot(t)
 d:=leadDraft{Kind:"existing",Name:"Иван",Phone:"+79991112233",LocationID:"001",Stage:"review"}
 if err:=b.leads.putDraft(100,d);err!=nil{t.Fatal(err)}
 lead,already,err:=b.leads.submit(100,"client","Метро Троицк")
 if err!=nil||already{t.Fatal(err)}
 b.notifyLeadAdmins(context.Background(),lead)
 if len(tr.requests)!=2{t.Fatalf("both admins must receive notification: %v",tr.requests)}
 for _,payload:=range tr.payloads {
  if payload["chat_id"]==float64(200)||payload["chat_id"]==float64(300){
   keyboard,ok:=payload["reply_markup"].(map[string]any)
   if !ok{t.Fatalf("notification missing buttons: %+v",payload)}
   if !strings.Contains(fmtMarkup(keyboard),"lead:take:1"){t.Fatalf("notification missing take callback %+v",keyboard)}
  }
 }
 unauthorized:=&callbackQuery{ID:"bad",Data:"lead:take:1",From:user{ID:999},Message:&message{MessageID:100,Chat:chat{ID:999,Type:"private"}}}
 if err:=b.handle(context.Background(),update{Callback:unauthorized});err!=nil{t.Fatal(err)}
 if current,_:=b.leads.submitted(100);current.TakenInWork{t.Fatal("non-admin claimed application")}
 authorized:=&callbackQuery{ID:"good",Data:"lead:take:1",From:user{ID:200},Message:&message{MessageID:100,Chat:chat{ID:200,Type:"private"}}}
 if err:=b.handle(context.Background(),update{Callback:authorized});err!=nil{t.Fatal(err)}
 current,_:=b.leads.submitted(100)
 if !current.TakenInWork||current.TakenBy!=200{t.Fatalf("admin claim not persisted %+v",current)}
 if adminLeadMarkup(current)["inline_keyboard"]==nil{t.Fatal("green button missing")}
 if !strings.Contains(fmtMarkup(adminLeadMarkup(current)),"success"){t.Fatal("green success style missing")}
 edits:=0
 for _,method:=range tr.requests{if method=="/botTEST/editMessageReplyMarkup"{edits++}}
 if edits<2{t.Fatalf("expected both admins' buttons updated: %+v",tr.requests)}
 if err:=b.handle(context.Background(),update{Callback:authorized});err!=nil{t.Fatal(err)}
 after,_:=b.leads.submitted(100)
 if after.TakenBy!=200{t.Fatal("repeat click changed owner")}
}

func fmtMarkup(v any) string {
 raw,_:=json.Marshal(v)
 return string(raw)
}
