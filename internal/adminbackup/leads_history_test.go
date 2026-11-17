package adminbackup

import (
 "encoding/json"
 "testing"
)

func TestMergeLeadsPreservesProcessedHistoryAndCurrentPending(t *testing.T) {
 archive:=[]byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100,"taken_in_work":false}},"next_id":1}`)
 current:=[]byte(`{"drafts":{},"submitted":{"100":{"number":2,"user_id":100,"taken_in_work":false}},"history":[{"number":1,"user_id":100,"taken_in_work":true,"taken_by":200}],"next_id":2}`)
 merged,err:=mergeLeads(archive,current)
 if err!=nil{t.Fatal(err)}
 result,err:=parseLeads(merged)
 if err!=nil{t.Fatal(err)}
 if result.NextID!=2||len(result.Submitted)!=1||len(result.History)!=1{t.Fatalf("lost application history: %s",merged)}
 var currentLead,previous struct {
  Number int64 `json:"number"`
  TakenInWork bool `json:"taken_in_work"`
 }
 if err=json.Unmarshal(result.Submitted["100"],&currentLead);err!=nil{t.Fatal(err)}
 if err=json.Unmarshal(result.History[0],&previous);err!=nil{t.Fatal(err)}
 if currentLead.Number!=2||currentLead.TakenInWork||previous.Number!=1||!previous.TakenInWork{
  t.Fatalf("restore re-blocked processed lead: %s",merged)
 }
}
func TestMergeLeadsKeepsOlderUnrelatedApplicants(t *testing.T) {
 archive:=[]byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100},"400":{"number":2,"user_id":400}},"next_id":2}`)
 current:=[]byte(`{"drafts":{},"submitted":{"100":{"number":3,"user_id":100,"taken_in_work":false},"200":{"number":2,"user_id":200}},"history":[{"number":1,"user_id":100,"taken_in_work":true}],"next_id":3}`)
 merged,err:=mergeLeads(archive,current)
 if err!=nil{t.Fatal(err)}
 state,err:=parseLeads(merged)
 if err!=nil{t.Fatal(err)}
 if len(state.Submitted)!=3||len(state.History)!=1||state.NextID!=4 {t.Fatalf("lost other client or failed collision handling %s",merged)}
 var older struct{Number int64 `json:"number"`}
 if err=json.Unmarshal(state.Submitted["400"],&older);err!=nil{t.Fatal(err)}
 if older.Number!=4{t.Fatalf("collision did not renumber archived lead %+v",older)}
}
