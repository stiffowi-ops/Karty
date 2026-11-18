package telegram

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"locationbot/internal/analytics"
)

func TestPersonalHomeActionLifecycle(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	action := func(label, data string) {
		t.Helper()
		got := b.homeLeadAction(100)
		if got["text"] != label || got["callback_data"] != data {
			t.Fatalf("action = %v, want %s, %s", got, label, data)
		}
	}
	action("📝 Оставить заявку", "lead:open:menu")
	if err := b.leads.putDraft(100, leadDraft{Stage: "name", Paused: true}); err != nil {
		t.Fatal(err)
	}
	action("▶️ Продолжить заявку", "lead:resume")
	l := serviceLead(t, b, 200)
	action("➕ Дополнить заявку", "cab:add:1")
	ctx := context.Background()
	if err := b.handleCabinetCallback(ctx, 100, user{ID: 100}, "cab:add:1"); err != nil {
		t.Fatal(err)
	}
	d, ok := b.leads.supplement(100)
	if !ok || d.Number != l.Number {
		t.Fatal("missing supplement draft")
	}
	action("➕ Дополнить заявку", supplementData("resume", d, ""))
	if got := b.homeLeadAction(101); got["callback_data"] != "lead:open:menu" {
		t.Fatalf("action leaked: %v", got)
	}
	if _, _, err := b.leads.resolveCase(l.Number, 200, 0, "Вопрос решён"); err != nil {
		t.Fatal(err)
	}
	action("📝 Оставить заявку", "lead:open:menu")
}

func TestClientEventsAndMetricDescriptions(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	var err error
	b.metrics, err = analytics.Open(filepath.Join(t.TempDir(), "analytics.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tap := func(userID int64, data string) {
		t.Helper()
		cb := callbackQuery{ID: "tap", From: user{ID: userID}, Message: &message{MessageID: 42, Chat: chat{ID: userID, Type: "private"}}, Data: data}
		if err := b.handle(ctx, update{Callback: &cb}); err != nil {
			t.Fatal(err)
		}
	}
	tap(100, "loc:001:0")
	tap(100, "loc:001:0")
	tap(101, "loc:001:0")
	tap(200, "loc:001:0")
	tap(100, "fav:toggle:001:0")
	tap(100, "fav:remove:001")
	tap(100, "fav:remove:001")
	tap(200, "fav:toggle:001:0")
	// A saved entry predating the event log is included in the current total.
	if _, err := b.favorites.Toggle(101, "001"); err != nil {
		t.Fatal(err)
	}
	l := serviceLead(t, b, 0)
	sendStoredSupplement(t, b, l.Number, []string{"002"}, "Вторая точка")

	for _, tc := range []struct {
		metric, description string
		value               int
	}{
		{"views", "Сколько раз клиенты открыли", 2},
		{"unique", "Сколько разных клиентов", 2},
		{"added", "добавление остаётся в статистике", 1},
		{"saved", "сохранена сейчас", 1},
		{"leads", "по дате их добавления", 1},
	} {
		if err := b.showAdminStats(ctx, 200, tc.metric, 7); err != nil {
			t.Fatal(err)
		}
		msg := tr.card["text"].(string)
		location := "Метро Троицк</b> — " + strconv.Itoa(tc.value)
		if !strings.Contains(msg, tc.description) || !strings.Contains(msg, location) {
			t.Fatalf("%s has incorrect description or count: %s", tc.metric, msg)
		}
		if tc.metric == "leads" && !strings.Contains(msg, "Ашан Марфино</b> — 1") {
			t.Fatal(msg)
		}
	}
	if n := len(b.metrics.Snapshot()); n != 4 {
		t.Fatalf("expected two views, one add, one removal, got %d events", n)
	}
}
