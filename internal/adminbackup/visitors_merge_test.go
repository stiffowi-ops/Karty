package adminbackup

import (
 "encoding/json"
 "testing"
 "time"
)

func TestMergeVisitorsDoesNotResendAlertOrLoseNewSession(t *testing.T){
 old:=[]byte(`{"100":{"user_id":100,"first_name":"Иван","first_at":"2026-11-01T09:00:00Z","last_at":"2026-11-01T10:00:00Z","daily":{"2026-11-01":2},"alerted":true,"alert_recipients":{"200":true}}}`)
 current:=[]byte(`{"100":{"user_id":100,"first_name":"Иван","username":"modern","first_at":"2026-11-02T09:00:00Z","last_at":"2026-11-17T10:00:00Z","daily":{"2026-11-17":15},"has_lead":true},"200":{"user_id":200,"first_at":"2026-11-17T09:00:00Z","last_at":"2026-11-17T10:00:00Z"}}`)
 merged,err:=mergeVisitors(old,current)
 if err!=nil{t.Fatal(err)}
 p,err:=parseVisitors(merged);if err!=nil{t.Fatal(err)}
 if len(p)!=2{t.Fatalf("lost new visitor: %s",merged)}
 u:=p["100"]
 if !u.HasLead || !u.Alerted || !u.AlertRecipients["200"] || u.Daily["2026-11-17"]!=15 || u.Username!="modern"{t.Fatalf("visitor state reset: %+v",u)}
 if !u.FirstAt.Equal(time.Date(2026,11,1,9,0,0,0,time.UTC)){t.Fatalf("first visit lost: %+v",u)}
}
func TestVisitorsBackupRejectsInvalidIDs(t *testing.T){
 _,err:=parseVisitors([]byte(`{"5":{"user_id":6}}`))
 if err==nil{t.Fatal("ID mismatch accepted")}
 _,err=parseVisitors([]byte(`{"5":{"user_id":5,"first_at":"2026-11-01T09:00:00Z","last_at":"2026-11-01T10:00:00Z","daily":{"2026-11-01":-1}}}`))
 if err==nil{t.Fatal("negative session count accepted")}
}
func TestMergeVisitorsCanRecoverArchivedOnly(t *testing.T){
 archived:=[]byte(`{"5":{"user_id":5,"first_at":"2026-11-01T09:00:00Z","last_at":"2026-11-01T10:00:00Z","daily":{"2026-11-01":1}}}`)
 output,err:=mergeVisitors(archived,nil)
 if err!=nil{t.Fatal(err)}
 var x map[string]json.RawMessage
 if err=json.Unmarshal(output,&x);err!=nil{t.Fatal(err)}
 if len(x)!=1{t.Fatalf("restore missing visitor: %s",output)}
}
