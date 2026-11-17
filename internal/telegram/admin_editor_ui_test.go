package telegram

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"locationbot/internal/catalog"
)

func TestAdminEditorFieldPromptsShowCurrentValues(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	ctx := context.Background()
	if err := b.startAdminEditor(ctx, 100, 100, "020"); err != nil {
		t.Fatal(err)
	}
	s := b.adminSession(100)
	s.Draft.LineStatus = "Линия · открыта"
	s.Draft.Landmarks = "Ориентир рядом"
	s.Draft.MapURL = "https://yandex.ru/maps/?ll=2"
	s.Draft.Description = "Описание действующей карточки"
	checks := map[string]string{"title": "Метро Академика Черенкова", "line_status": "Линия · открыта", "landmarks": "Ориентир рядом", "map_url": "https://yandex.ru/maps/?ll=2", "description": "Описание действующей карточки"}
	for field, old := range checks {
		if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:field:"+field); err != nil {
			t.Fatal(err)
		}
		msg, _ := lastAdminMessage(t, tr)
		if !strings.Contains(msg, "Текущее значение:") || !strings.Contains(msg, old) {
			t.Fatalf("field %q omitted old value %q: %q", field, old, msg)
		}
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:sectiontitle:0"); err != nil {
		t.Fatal(err)
	}
	title, _ := lastAdminMessage(t, tr)
	if !strings.Contains(title, "Текущее значение:") || !strings.Contains(title, s.Draft.Sections[0].Title) {
		t.Fatalf("section title old value missing: %q", title)
	}
	s.Draft.Sections[0].Body = "Аудитория & поток < 100"
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:sectionbody:0"); err != nil {
		t.Fatal(err)
	}
	body, _ := lastAdminMessage(t, tr)
	if !strings.Contains(body, "Аудитория &amp; поток &lt; 100") || strings.Contains(body, "Аудитория & поток < 100") {
		t.Fatalf("unsafe or missing section body: %q", body)
	}
}

func TestAdminLocationListAndSearchTwoColumns(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	extra := catalog.Location{ID: "custom-aabb1122", Title: "Метро Лесное", Sections: []catalog.CardSection{}}
	if err := b.store.CreateAdminLocation(extra); err != nil {
		t.Fatal(err)
	}
	if err := b.showAdminLocations(context.Background(), 100, 0); err != nil {
		t.Fatal(err)
	}
	_, raw := lastAdminMessage(t, tr)
	var markup struct {
		InlineKeyboard [][]map[string]any `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(raw), &markup); err != nil {
		t.Fatal(err)
	}
	if len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("location list must show two columns: %+v", markup)
	}
	buttons := string([]byte(raw))
	if !strings.Contains(buttons, "adm:new") || !strings.Contains(buttons, "adm:locsearch") {
		t.Fatalf("missing create/search controls: %s", raw)
	}
	session := b.adminSession(100)
	session.Field = "search_location"
	if handled, err := b.handleAdminText(context.Background(), 100, user{ID: 100}, "Метро"); !handled || err != nil {
		t.Fatalf("search failed: %v %v", handled, err)
	}
	_, raw = lastAdminMessage(t, tr)
	if err := json.Unmarshal([]byte(raw), &markup); err != nil {
		t.Fatal(err)
	}
	if len(markup.InlineKeyboard[0]) != 2 {
		t.Fatalf("admin search results not in 2 columns: %s", raw)
	}
}

func TestAdminNewLocationDraftPublishAndCancel(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	ctx := context.Background()
	if err := b.handleAdminCallback(ctx, 101, user{ID: 101}, "adm:new"); err != nil {
		t.Fatal(err)
	}
	if b.adminStates != nil && b.adminStates[101] != nil {
		t.Fatal("unauthorized admin draft created")
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:new"); err != nil {
		t.Fatal(err)
	}
	session := b.adminSession(100)
	if !session.CreatingLocation || session.Field != "new_title" {
		t.Fatalf("creation wizard not started: %+v", session)
	}
	id := session.LocationID
	if _, found := b.store.Find(id); found {
		t.Fatal("new draft appeared in catalog")
	}
	if handled, err := b.handleAdminText(ctx, 100, user{ID: 100}, "Метро НОВАЯ"); !handled || err != nil {
		t.Fatalf("wizard failed: %v %v", handled, err)
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:field:description"); err != nil {
		t.Fatal(err)
	}
	msg, _ := lastAdminMessage(t, tr)
	if !strings.Contains(msg, "Текущее значение:") || !strings.Contains(msg, "Не заполнено") {
		t.Fatal("missing current empty value")
	}
	if handled, err := b.handleAdminText(ctx, 100, user{ID: 100}, "Новая точка бизнеса"); !handled || err != nil {
		t.Fatalf("editing description failed: %v %v", handled, err)
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:add"); err != nil {
		t.Fatal(err)
	}
	session.Draft.Sections[0].Title = "🏪 Новый раздел"
	session.Draft.Sections[0].Body = "Описание раздела"
	if err := b.renderAdminPreview(ctx, 100, session); err != nil {
		t.Fatalf("preview for new draft failed: %v", err)
	}
	if _, found := b.store.Find(id); found {
		t.Fatal("preview published a draft")
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:publish"); err != nil {
		t.Fatal(err)
	}
	got, found := b.store.Find(id)
	if !found || got.Title != "Метро НОВАЯ" || got.Description != "Новая точка бизнеса" || len(got.Sections) != 1 {
		t.Fatalf("new card not published: %+v", got)
	}
	if session.CreatingLocation {
		t.Fatal("editing session still flagged as draft")
	}
	if err := b.store.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, found := b.store.Find(id); !found {
		t.Fatal("new card lost after reload")
	}
	bytes, err := os.ReadFile(filepath.Join(b.store.DataDir(), catalog.AdminLocationsFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bytes), id) {
		t.Fatal("new location missing from permanent data")
	}
	history, err := b.adminHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Operation != "create" {
		t.Fatalf("creation history missing: %+v", history)
	}
	if err := b.showAdminHistoryEntry(ctx, 100, 0); err != nil {
		t.Fatal(err)
	}
	_, keyboard := lastAdminMessage(t, tr)
	if strings.Contains(keyboard, "adm:histrestore:0") {
		t.Fatalf("unsafe rollback of creation offered: %s", keyboard)
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:new"); err != nil {
		t.Fatal(err)
	}
	cancelled := b.adminSession(100).LocationID
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:locations:0"); err != nil {
		t.Fatal(err)
	}
	if _, found := b.store.Find(cancelled); found {
		t.Fatal("cancelled new draft published")
	}
}

func TestShortAdminMenuNoStatsOrBrandSuffix(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	if err := b.showAdminMenu(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	msg, _ := lastAdminMessage(t, tr)
	if strings.Contains(msg, "Karty") || strings.Contains(msg, "Локаций:") || strings.Contains(msg, "Заявок:") || !strings.Contains(msg, "редактировать и добавлять локации") {
		t.Fatalf("admin description should be brief without counters: %s", msg)
	}
}
