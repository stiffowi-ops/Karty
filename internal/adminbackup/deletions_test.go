package adminbackup

import (
 "encoding/json"
 "os"
 "path/filepath"
 "testing"
 "time"

 "locationbot/internal/catalog"
)

func TestDeletionLedgerAndFilters(t *testing.T) {
 root:=t.TempDir()
 if err:=MarkDeleted(root,[]int64{1},[]int64{100});err!=nil{t.Fatal(err)}
 marked,err:=ReadDeletionLedger(root);if err!=nil{t.Fatal(err)}
 if len(marked.Leads)!=1||len(marked.Visitors)!=1{t.Fatalf("missing tombstones %+v",marked)}
 oldLeads:=[]byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100,"taken_in_work":true},"200":{"number":2,"user_id":200,"taken_in_work":true}},"history":[],"next_id":2}`)
 filtered,err:=FilterDeletedLeads(oldLeads,marked);if err!=nil{t.Fatal(err)}
 state,err:=parseLeads(filtered);if err!=nil{t.Fatal(err)}
 if len(state.Submitted)!=1||state.NextID!=2{t.Fatalf("lead deletion fails: %s",filtered)}
 oldVisitors:=[]byte(`{"100":{"user_id":100,"first_at":"2026-11-01T09:00:00Z","last_at":"2026-11-01T10:00:00Z"},"200":{"user_id":200,"first_at":"2026-11-01T09:00:00Z","last_at":"2026-11-01T10:00:00Z"}}`)
 users,err:=FilterDeletedVisitors(oldVisitors,marked);if err!=nil{t.Fatal(err)}
 var parsed map[string]json.RawMessage
 if err=json.Unmarshal(users,&parsed);err!=nil{t.Fatal(err)}
 if len(parsed)!=1||parsed["200"]==nil{t.Fatalf("visitor deletion fails: %s",users)}
}

func TestRestoringOldBackupCannotResurrectPermanentDeletion(t *testing.T){
 root:=t.TempDir()
 current:=[]catalog.Location{{ID:"001",Title:"Метро Троицк"}}
 if err:=os.WriteFile(filepath.Join(root,"leads.json"),[]byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100,"taken_in_work":true}},"next_id":1}`),0600);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(root,"visitors.json"),[]byte(`{"100":{"user_id":100,"first_at":"2026-11-01T09:00:00Z","last_at":"2026-11-01T10:00:00Z"}}`),0600);err!=nil{t.Fatal(err)}
 old,err:=Build(root,current);if err!=nil{t.Fatal(err)}
 archive,err:=Examine(old);if err!=nil{t.Fatal(err)}
 if err:=MarkDeleted(root,[]int64{1},[]int64{100});err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(root,"leads.json"),[]byte(`{"drafts":{},"submitted":{},"history":[],"next_id":1}`),0600);err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(root,"visitors.json"),[]byte("{}"),0600);err!=nil{t.Fatal(err)}
 if _,err:=Restore(root,archive,"full",current);err!=nil{t.Fatal(err)}
 merged,err:=os.ReadFile(filepath.Join(root,"leads.json"));if err!=nil{t.Fatal(err)}
 state,err:=parseLeads(merged);if err!=nil{t.Fatal(err)}
 if len(state.Submitted)!=0||len(state.History)!=0{t.Fatalf("deleted lead resurrected: %s",merged)}
 seen,err:=os.ReadFile(filepath.Join(root,"visitors.json"));if err!=nil{t.Fatal(err)}
 var vis map[string]json.RawMessage
 if err=json.Unmarshal(seen,&vis);err!=nil{t.Fatal(err)}
 if len(vis)!=0{t.Fatalf("deleted visitor resurrected: %s",seen)}
 marker,err:=ReadDeletionLedger(root);if err!=nil{t.Fatal(err)}
 if len(marker.Leads)!=1||len(marker.Visitors)!=1{t.Fatalf("ledger lost: %+v",marker)}
}

func TestDeletionMarkerUnionsWithBackup(t *testing.T) {
 now:=time.Now().UTC()
 before,_:=json.Marshal(DeletionLedger{Leads:map[string]time.Time{"1":now},Visitors:map[string]time.Time{}})
 after,_:=json.Marshal(DeletionLedger{Leads:map[string]time.Time{"2":now},Visitors:map[string]time.Time{"100":now}})
 merged,err:=mergeDeletionLedgers(before,after)
 if err!=nil{t.Fatal(err)}
 if len(merged.Leads)!=2||len(merged.Visitors)!=1{t.Fatalf("ledger union failed: %+v",merged)}
}
