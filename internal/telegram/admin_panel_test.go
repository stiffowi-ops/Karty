package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"locationbot/internal/analytics"
	"locationbot/internal/catalog"
)

type adminTransport struct {
	calls        []string
	payloads     []map[string]any
	lastDocument []byte
}

func (tr *adminTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.calls = append(tr.calls, r.URL.Path)
	data, _ := io.ReadAll(r.Body)
	if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
		tr.lastDocument = data
	} else {
		var p map[string]any
		_ = json.Unmarshal(data, &p)
		tr.payloads = append(tr.payloads, p)
	}
	result := `{"ok":true,"result":{"message_id":200}}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
}
func makeAdminTestBot(t *testing.T) (*Bot, *adminTransport) {
	t.Helper()
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"020","title":"Метро Академика Черенкова","description":"Исходный текст","line_status":"Троицкая линия","traffic":"Поток","payers":"Жители"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.New(dir, seed, filepath.Join(dir, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	favorites, err := openFavoriteStore(filepath.Join(dir, "favorites.json"))
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := analytics.Open(filepath.Join(dir, "analytics.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &adminTransport{}
	b := &Bot{token: "TEST", store: store, favorites: favorites, adminIDs: map[int64]bool{100: true}, metrics: metrics, http: &http.Client{Transport: tr}}
	return b, tr
}
func TestAdminAccessAndEditorPublishing(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	ctx := context.Background()
	if b.isAdmin(101) || !b.isAdmin(100) {
		t.Fatal("ADMIN_IDS gate broken")
	}
	userMarkup := b.adminHomeMarkup("https://example.com", 101)
	raw, _ := json.Marshal(userMarkup)
	if bytes.Contains(raw, []byte("adm:menu")) {
		t.Fatal("admin button leaked to user")
	}
	adminMarkup := b.adminHomeMarkup("https://example.com", 100)
	raw, _ = json.Marshal(adminMarkup)
	if !bytes.Contains(raw, []byte("adm:menu")) {
		t.Fatal("admin button absent")
	}
	if err := b.handleAdminCallback(ctx, 101, user{ID: 101}, "adm:menu"); err != nil {
		t.Fatal(err)
	}
	if len(tr.calls) != 0 {
		t.Fatal("unauthorized user got a response or state change")
	}
	if err := b.startAdminEditor(ctx, 100, 100, "020"); err != nil {
		t.Fatal(err)
	}
	sess := b.adminSession(100)
	sess.Field = "title"
	if handled, err := b.handleAdminText(ctx, 100, user{ID: 100}, "Метро Новая Черенкова"); !handled || err != nil {
		t.Fatalf("edit failed %v %v", handled, err)
	}
	sess.Draft.Sections = append(sess.Draft.Sections, catalog.CardSection{ID: "extra-one", Title: "🏪 Форматы бизнеса", Body: "Новое описание", Enabled: true})
	if err := b.publishAdminDraft(ctx, 100, 100, sess); err != nil {
		t.Fatal(err)
	}
	l, ok := b.store.Find("020")
	if !ok || l.Title != "Метро Новая Черенкова" || len(l.Sections) != 5 {
		t.Fatalf("card not published %+v", l)
	}
	if err := b.store.Reload(); err != nil {
		t.Fatal(err)
	}
	l, _ = b.store.Find("020")
	if l.Title != "Метро Новая Черенкова" {
		t.Fatal("revision lost after reload")
	}
	history, _ := b.adminHistory()
	if len(history) != 1 || history[0].Actor != 100 {
		t.Fatalf("history %+v", history)
	}
	if err := b.undoLastAdminChange(ctx, 100, 100); err != nil {
		t.Fatal(err)
	}
	l, _ = b.store.Find("020")
	if l.Title != "Метро Академика Черенкова" {
		t.Fatalf("undo failed %+v", l)
	}
}
func TestAdminStatsExcludeAdminViewsAndReload(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	now := time.Now().UTC()
	b.recordLocationAnalytics(100, "020", analytics.View)
	b.recordLocationAnalytics(777, "020", analytics.View)
	b.recordLocationAnalytics(777, "020", analytics.FavoriteAdded)
	if got := len(b.metrics.Snapshot()); got != 2 {
		t.Fatalf("analytics stored admin event or missed a public event %d", got)
	}
	if err := b.showAdminStats(context.Background(), 100, "views", 7); err != nil {
		t.Fatal(err)
	}
	if len(tr.payloads) == 0 || !strings.Contains(tr.payloads[len(tr.payloads)-1]["text"].(string), "Метро Академика Черенкова") {
		t.Fatal("missing stats")
	}
	_ = now
}
func TestAdminBackupExportIsZIPDocument(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	if err := b.sendAdminBackup(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if len(tr.calls) == 0 || !strings.Contains(tr.calls[len(tr.calls)-1], "/sendDocument") {
		t.Fatalf("backup not sent as Telegram document %v", tr.calls)
	}
	if !bytes.Contains(tr.lastDocument, []byte("karty-backup-")) {
		t.Fatal("missing archive filename")
	}
}
