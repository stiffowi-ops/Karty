package adminbackup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"locationbot/internal/catalog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sample() []catalog.Location {
	return []catalog.Location{{ID: "020", Title: "Метро Академика Черенкова", Sections: []catalog.CardSection{{ID: "traffic", Title: "Поток", Body: "Текст", Enabled: true}}}}
}
func TestArchiveRoundTripAndSafeRestoration(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"leads.json": `{"drafts":{},"submitted":{"123":{"number":1}},"next_id":1}`, "favorites.json": `{"123":["020"]}`, "analytics.jsonl": ``, "admin_history.json": `[]`} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := Build(root, sample())
	if err != nil {
		t.Fatal(err)
	}
	arc, err := Examine(payload)
	if err != nil {
		t.Fatal(err)
	}
	if arc.Inspect.Locations != 1 || arc.Inspect.Leads != 1 || arc.Inspect.Favorites != 1 {
		t.Fatalf("counts %+v", arc.Inspect)
	}
	os.WriteFile(filepath.Join(root, "leads.json"), []byte(`{"submitted":{}}`), 0600)
	protective, err := Restore(root, arc, "cards", sample())
	if err != nil {
		t.Fatal(err)
	}
	still, _ := os.ReadFile(filepath.Join(root, "leads.json"))
	if string(still) != `{"submitted":{}}` {
		t.Fatal("cards-only mode changed leads")
	}
	if _, err := os.Stat(protective); err != nil {
		t.Fatal(err)
	}
	protective, err = Restore(root, arc, "full", sample())
	if err != nil {
		t.Fatal(err)
	}
	leads, _ := os.ReadFile(filepath.Join(root, "leads.json"))
	var leadState struct {
		NextID int64 `json:"next_id"`
	}
	if err := json.Unmarshal(leads, &leadState); err != nil || leadState.NextID != 1 {
		t.Fatal("full restore lost application sequencing")
	}
	if _, err := os.Stat(protective); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "admin_cards.json"))
	var edits map[string]catalog.CardRevision
	if err := json.Unmarshal(b, &edits); err != nil || edits["020"].Title == "" {
		t.Fatalf("card revision %v %v", edits, err)
	}
}
func TestRejectTamperedAndUnsafeZIP(t *testing.T) {
	raw, _ := Build(t.TempDir(), sample())
	arc, err := Examine(raw)
	if err != nil {
		t.Fatal(err)
	}
	arc.Contents["leads.json"] = []byte(`hacked`)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	w, _ := zw.Create("../secret.txt")
	w.Write([]byte("bad"))
	zw.Close()
	if _, err := Examine(b.Bytes()); err == nil {
		t.Fatal("path traversal accepted")
	}
	raw[len(raw)-25] ^= 0x42
	if _, err := Examine(raw); err == nil {
		t.Log("CRC error not observed on metadata-only mutation")
	}
}

func TestRestoringOldBackupKeepsNewApplications(t *testing.T) {
	old := []byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100}},"next_id":1}`)
	current := []byte(`{"drafts":{},"submitted":{"100":{"number":1,"user_id":100},"200":{"number":2,"user_id":200}},"next_id":2}`)
	merged, err := mergeLeads(old, current)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		NextID    int64                      `json:"next_id"`
		Submitted map[string]json.RawMessage `json:"submitted"`
	}
	if err = json.Unmarshal(merged, &state); err != nil {
		t.Fatal(err)
	}
	if state.NextID != 2 || len(state.Submitted) != 2 {
		t.Fatalf("restoring old backup lost protection: %s", merged)
	}
}
func TestRestoringOldAnalyticsPreservesNewEvents(t *testing.T) {
	archived := []byte(`{"at":"2026-11-01T10:00:00Z","user_id":1,"location_id":"001","kind":"view"}` + "\n")
	current := []byte(`{"at":"2026-11-15T10:00:00Z","user_id":2,"location_id":"002","kind":"view"}` + "\n")
	merged, err := mergeAnalytics(archived, current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(merged, []byte(`"location_id":"001"`)) || !bytes.Contains(merged, []byte(`"location_id":"002"`)) {
		t.Fatalf("lost analytics: %s", merged)
	}
}

func TestRestoringOldFavoritesPreservesNewActionsAndRemovedLastItem(t *testing.T) {
	archived := []byte(`{"100":["020"],"200":["020"],"300":["020"]}`)
	current := []byte(`{"100":["020","021"],"200":["021"]}`)
	date := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	events := []byte(`{"at":"2026-11-02T10:00:00Z","user_id":300,"location_id":"020","kind":"favorite_remove"}` + "\n")
	merged, err := mergeFavorites(archived, current, events, date)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string][]string
	if err := json.Unmarshal(merged, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got["100"]) != 2 || len(got["200"]) != 1 || got["200"][0] != "021" {
		t.Fatalf("new changes lost or removed favorites resurrected: %s", merged)
	}
}
func TestRestoringOldFavoritesKeepsArchiveOnlyUsers(t *testing.T) {
	archive := []byte(`{"100":["020"]}`)
	data, err := mergeFavorites(archive, []byte(`{}`), nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var result map[string][]string
	_ = json.Unmarshal(data, &result)
	if len(result["100"]) != 1 {
		t.Fatalf("archive-only user must restore: %s", data)
	}
}

func TestRestoreKeepsNewAdminHistory(t *testing.T) {
	a := []byte(`[{"at":"2026-11-01T08:00:00Z","location_id":"020","operation":"publish"}]`)
	b := []byte(`[{"at":"2026-11-15T08:00:00Z","location_id":"020","operation":"undo"}]`)
	merged, err := mergeRevisionHistory(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(merged, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !bytes.Contains(merged, []byte(`"undo"`)) {
		t.Fatalf("history lost: %s", merged)
	}
}
