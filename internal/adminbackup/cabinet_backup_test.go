package adminbackup

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRestorePreservesCaseCompletionAndDoesNotResurrectSentSupplement(t *testing.T) {
	archive := []byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100,"taken_in_work":true}},"supplements":{"100":{"number":1,"token":"sent","stage":"review","comment":"старый черновик"}},"next_id":1}`)
	current := []byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100,"taken_in_work":true,"resolved_at":"2026-11-17T15:00:00Z","resolution":"Готово","updates":[{"id":1,"token":"sent","comment":"Уточнение","accepted_at":"2026-11-17T14:00:00Z"}],"notice_receipts":{"client:resolved":true}}},"next_id":1}`)
	merged, err := mergeLeads(archive, current)
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseLeads(merged)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Supplements) != 0 {
		t.Fatalf("completed/sent draft resurrected: %s", merged)
	}
	for _, field := range []string{"resolved_at", "accepted_at", "client:resolved", "Уточнение"} {
		if !strings.Contains(string(s.Submitted["100"]), field) {
			t.Fatalf("case field %s lost: %s", field, merged)
		}
	}
}
func TestRestoreKeepsCurrentSupplementDraftAndCompletionHistory(t *testing.T) {
	archive := []byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100,"taken_in_work":false}},"next_id":1}`)
	current := []byte(`{"drafts":{},"submitted":{"100":{"number":2,"user_id":100,"taken_in_work":true}},"history":[{"number":1,"user_id":100,"resolved_at":"2026-11-17T15:00:00Z","resolution":"Решено"}],"supplements":{"100":{"number":2,"token":"current","stage":"comment","locations":[{"id":"002","name":"Локация"}]}},"next_id":2}`)
	merged, err := mergeLeads(archive, current)
	if err != nil {
		t.Fatal(err)
	}
	s, err := parseLeads(merged)
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(s.Supplements["100"], &d)
	if d.Token != "current" || len(s.History) != 1 || !strings.Contains(string(s.History[0]), "resolved_at") {
		t.Fatalf("current case/draft lost: %s", merged)
	}
}
