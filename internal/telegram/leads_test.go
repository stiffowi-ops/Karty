package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"locationbot/internal/catalog"
)

func TestLeadStoreSingleSubmissionAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leads.json")
	store, err := openLeadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	draft := leadDraft{Kind: "new", Company: "ООО Пример", Name: "Иван", Phone: "+7 999 123 45 67", LocationID: "001", Stage: "review", Source: "card"}
	if err := store.putDraft(100, draft); err != nil {
		t.Fatal(err)
	}
	reopened, err := openLeadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reopened.draft(100); !ok || got.Company != "ООО Пример" {
		t.Fatalf("draft not restored: %+v %v", got, ok)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, existing := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, already, err := reopened.submit(100, "testuser", "Метро Троицк")
			if err != nil {
				t.Error(err)
				return
			}
			if l.Number != 1 {
				t.Errorf("bad lead number %d", l.Number)
			}
			mu.Lock()
			if already {
				existing++
			} else {
				created++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if created != 1 || existing != 19 {
		t.Fatalf("duplicate submissions: %d created, %d existing", created, existing)
	}
	reloaded, err := openLeadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.draft(100); ok {
		t.Fatal("draft not removed after submission")
	}
	if got, ok := reloaded.submitted(100); !ok || got.LocationID != "001" || got.UserID != 100 {
		t.Fatalf("submission not restored: %+v %v", got, ok)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("storage file permissions: %v %v", info, err)
	}
}

func makeLeadTestBot(t *testing.T) (*Bot, *captureTransport) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "seed.json")
	seed := `[{"id":"001","title":"Метро Троицк"},{"id":"002","title":"Ашан Марфино"},{"id":"003","title":"МГСУ"}]`
	if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.New(root, path, filepath.Join(root, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	fav, err := openFavoriteStore(filepath.Join(root, "favorites.json"))
	if err != nil {
		t.Fatal(err)
	}
	leads, err := openLeadStore(filepath.Join(root, "leads.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &captureTransport{}
	return &Bot{token: "TEST", store: store, favorites: fav, leads: leads, adminIDs: map[int64]bool{200: true, 300: true}, http: &http.Client{Transport: tr}}, tr
}

func TestLeadFlowFromCardAndNotifyEveryAdmin(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	ctx := context.Background()
	u := user{ID: 100, Username: "lead100"}
	chatID := int64(100)
	if err := b.startLead(ctx, chatID, u.ID, "card", "001"); err != nil {
		t.Fatal(err)
	}
	for _, cb := range []string{"lead:kind:existing"} {
		if err := b.handleLeadCallback(ctx, chatID, u, cb); err != nil {
			t.Fatal(err)
		}
	}
	if handled, err := b.handleLeadText(ctx, chatID, u, "Александр"); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if handled, err := b.handleLeadText(ctx, chatID, u, "+7 999 888-77-66"); !handled || err != nil {
		t.Fatal(handled, err)
	}
	d, ok := b.leads.draft(u.ID)
	if !ok || d.Stage != "location_confirm" || d.LocationID != "001" {
		t.Fatalf("location not preselected: %+v", d)
	}
	if err := b.handleLeadCallback(ctx, chatID, u, "lead:confirm"); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(ctx, chatID, u, "lead:submit"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.leads.submitted(u.ID); !ok {
		t.Fatal("lead not submitted")
	}
	if err := b.handleLeadCallback(ctx, chatID, u, "lead:submit"); err != nil {
		t.Fatal(err)
	}
	if len(b.leads.allSubmitted()) != 1 {
		t.Fatal("second application was created")
	}
	l, _ := b.leads.submitted(u.ID)
	if !l.Delivered["200"] || !l.Delivered["300"] {
		t.Fatalf("some admins not notified: %+v", l.Delivered)
	}
	adminCalls := 0
	for _, p := range tr.payloads {
		if _, ok := p["chat_id"].(float64); ok {
			if p["chat_id"] == float64(200) || p["chat_id"] == float64(300) {
				adminCalls++
			}
		}
	}
	if adminCalls != 2 {
		t.Fatalf("expected two admin notifications, got %d", adminCalls)
	}
}

func TestFavoritesLeadLocationSelectionRestricted(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	ctx := context.Background()
	u := user{ID: 100}
	if _, err := b.favorites.Toggle(u.ID, "002"); err != nil {
		t.Fatal(err)
	}
	if err := b.startLead(ctx, u.ID, u.ID, "favorites", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(ctx, u.ID, u, "lead:kind:new"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ООО Перспектива", "Ирина", "+7 999 888-77-66"} {
		if ok, err := b.handleLeadText(ctx, u.ID, u, field); !ok || err != nil {
			t.Fatalf("field %q: %v", field, err)
		}
	}
	if err := b.handleLeadCallback(ctx, u.ID, u, "lead:pick:001"); err != nil {
		t.Fatal(err)
	}
	d, _ := b.leads.draft(u.ID)
	if d.LocationID != "" {
		t.Fatalf("unfavorited location accepted: %+v", d)
	}
	if err := b.handleLeadCallback(ctx, u.ID, u, "lead:pick:002"); err != nil {
		t.Fatal(err)
	}
	d, _ = b.leads.draft(u.ID)
	if d.LocationID != "002" {
		t.Fatalf("favorite not selected: %+v", d)
	}
}

func TestLeadPhoneValidationAndFriendlyCopy(t *testing.T) {
	for _, bad := range []string{
		"123", "test", "+777xyz12345", "+7 99",
		"+7906528893", "+790652889351", "+7 906 528 89 35 6",
		"+1 (415) 555-1234", "89065288935", "79065288935",
		"+7 906 528-89-3X", "+7++9065288935", "+7 906 528 89 35 доб",
	} {
		if validLeadPhone(bad) {
			t.Errorf("accepted invalid Russian phone %q", bad)
		}
		if normalized, ok := normalizeLeadPhone(bad); ok {
			t.Errorf("normalized invalid %q to %q", bad, normalized)
		}
	}
	for _, valid := range []string{
		"+79065288935", "+7 906 528-89-35",
		"+7 (906) 528 89 35", "  +7 906 528-89-35  ",
	} {
		normalized, ok := normalizeLeadPhone(valid)
		if !ok || normalized != "+79065288935" || !validLeadPhone(valid) {
			t.Errorf("rejected or incorrectly normalized %q => %q (ok=%v)", valid, normalized, ok)
		}
	}
	lead := leadApplication{Number: 1, UserID: 45, Kind: "new", Company: "ООО <script>", Name: "Ирина", Phone: "+7 999 123-45-67", LocationName: "Метро Троицк", Source: "card"}
	txt := adminLeadText(lead)
	if !strings.Contains(txt, "Заявка №0001") || strings.Contains(txt, "· Karty") || strings.Contains(txt, "Новая заявка") {
		t.Fatalf("unexpected admin lead heading: %s", txt)
	}
	if strings.Contains(txt, "<script>") || !strings.Contains(txt, "&lt;script&gt;") {
		t.Fatalf("unsafe admin HTML: %s", txt)
	}
	if _, err := json.Marshal(lead); err != nil {
		t.Fatal(err)
	}
}

func TestAdminIDsAndLeadEntryButtons(t *testing.T) {
	dir := t.TempDir()
	b, err := New("TEST", "", dir, nil, nil, "111, 222,111,notanid")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.adminIDs) != 2 || !b.adminIDs[111] || !b.adminIDs[222] {
		t.Fatalf("expected two unique admin recipients: %+v", b.adminIDs)
	}
	if b.leads == nil {
		t.Fatal("lead storage was not initialized")
	}
	if got := leadButton("card", "001")["callback_data"]; got != "lead:open:card:001" {
		t.Fatalf("unexpected card button: %v", got)
	}
	if got := leadButton("favorites", "")["callback_data"]; got != "lead:open:favorites" {
		t.Fatalf("unexpected favorite button: %v", got)
	}
}

func TestLeadEmptyFavoritesDoesNotStartWizard(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	if err := b.openLead(context.Background(), 101, 101, "favorites", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.leads.draft(101); ok {
		t.Fatal("wizard started with no favorite locations")
	}
}