package telegram

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func prepareVisitor(t *testing.T, b *Bot, id int64) time.Time {
	t.Helper()
	if b.visitors == nil {
		v, err := openVisitorStore(filepath.Join(t.TempDir(), "visitors.json"))
		if err != nil {
			t.Fatal(err)
		}
		b.visitors = v
	}
	at := time.Now().UTC().Add(-2 * time.Hour)
	if err := b.visitors.record(user{ID: id, FirstName: "Посетитель", Username: "visitor_test"}, "Главное меню", true, at); err != nil {
		t.Fatal(err)
	}
	return at
}
func TestVisitorAlertIncludesCurrentFavoritesAndEmptyMarker(t *testing.T) {
	for _, withFavorites := range []bool{true, false} {
		t.Run(fmt.Sprint(withFavorites), func(t *testing.T) {
			b, tr := makeLeadTestBot(t)
			prepareVisitor(t, b, 88)
			if withFavorites {
				if _, err := b.favorites.Toggle(88, "001"); err != nil {
					t.Fatal(err)
				}
				if _, err := b.favorites.Remove(88, "001"); err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"002", "003"} {
					if _, err := b.favorites.Toggle(88, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			b.sendVisitorAlerts(context.Background(), time.Now())
			if len(tr.payloads) != 2 {
				t.Fatalf("expected one alert per administrator, got %d", len(tr.payloads))
			}
			for _, p := range tr.payloads {
				text := p["text"].(string)
				if !strings.Contains(text, "Избранное") {
					t.Fatal("favorites section absent")
				}
				if withFavorites {
					for _, want := range []string{"Ашан Марфино", "МГСУ", "002", "003"} {
						if !strings.Contains(text, want) {
							t.Fatalf("missing %q in %s", want, text)
						}
					}
					if strings.Contains(text, "Троицк") {
						t.Fatal("removed favorite included")
					}
				} else if !strings.Contains(text, "Ничего не добавлено в избранное") {
					t.Fatal("empty favorites marker absent")
				}
				if !strings.Contains(fmtMarkup(p["reply_markup"]), "adm:visitorfavs:88:0") {
					t.Fatal("full favorites button absent")
				}
			}
			b.sendVisitorAlerts(context.Background(), time.Now().Add(time.Hour))
			restored, err := openVisitorStore(b.visitors.path)
			if err != nil {
				t.Fatal(err)
			}
			b.visitors = restored
			b.sendVisitorAlerts(context.Background(), time.Now().Add(2*time.Hour))
			if len(tr.payloads) != 2 {
				t.Fatal("updated visitor alarm repeated after restart")
			}
		})
	}
}
func TestVisitorFavoritesPrivatePagingAndMissingCatalogEntry(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	prepareVisitor(t, b, 88)
	for i := 1; i <= 25; i++ {
		if _, err := b.favorites.Toggle(88, fmt.Sprintf("%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := b.visitors.find(88)
	items := b.visitorFavorites(88)
	preview := b.visitorAlertHTML(p, time.Now(), items)
	if !strings.Contains(preview, "Ещё 13") || !strings.Contains(preview, "Избранное · 25") {
		t.Fatalf("long list not accounted for: %s", preview)
	}
	before := len(tr.payloads)
	if err := b.handleAdminCallback(context.Background(), 88, user{ID: 88}, "adm:visitorfavs:88:0"); err != nil {
		t.Fatal(err)
	}
	if len(tr.payloads) != before {
		t.Fatal("client accessed visitor favorites")
	}
	for page, expected := range []int{10, 10, 5} {
		if err := b.handleAdminCallback(context.Background(), 200, user{ID: 200}, fmt.Sprintf("adm:visitorfavs:88:%d", page)); err != nil {
			t.Fatal(err)
		}
		text := tr.card["text"].(string)
		if strings.Count(text, "• ") != expected {
			t.Fatalf("page %d: missing favorites: %s", page, text)
		}
	}
	if !strings.Contains(tr.card["text"].(string), "отсутствует в текущем каталоге") {
		t.Fatal("unknown saved IDs were silently dropped")
	}
}
func TestVisitorAlertStaysWithinTelegramLimitWithLongRouteAndFavorites(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	prepareVisitor(t, b, 88)
	p, _ := b.visitors.find(88)
	p.FirstName = strings.Repeat("😀", 80)
	p.LastName = p.FirstName
	p.Username = strings.Repeat("x", 40)
	for i := 0; i < 35; i++ {
		p.FirstRoute = append(p.FirstRoute, visitorStep{At: p.FirstAt, Name: strings.Repeat("😀", 80)})
	}
	items := []visitorFavorite{}
	for i := 1; i <= 114; i++ {
		items = append(items, visitorFavorite{id: fmt.Sprintf("%03d", i), name: strings.Repeat("😀", 80)})
	}
	body := b.visitorAlertHTML(p, time.Now(), items)
	visible := html.UnescapeString(regexp.MustCompile(`<[^>]*>`).ReplaceAllString(body, ""))
	if utf16Len(visible) > 4096 {
		t.Fatalf("alarm too long: %d", utf16Len(visible))
	}
	if !strings.Contains(body, "Ещё 102") || !strings.Contains(body, "шагов — в карточке") {
		t.Fatal("truncated list/route not explained")
	}
	if len(p.FirstRoute) != 36 || p.FirstRoute[0].Name != "Главное меню" {
		t.Fatal("alert formatting mutated stored route")
	}
}
func TestVisitorAlertFavoritesRetryAndSubmittedLeadExclusion(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	prepareVisitor(t, b, 88)
	if _, err := b.favorites.Toggle(88, "003"); err != nil {
		t.Fatal(err)
	}
	tr := &caseTransport{failAdmin: 300, failOnce: true}
	b.http = &http.Client{Transport: tr}
	b.sendVisitorAlerts(context.Background(), time.Now())
	b.sendVisitorAlerts(context.Background(), time.Now())
	if tr.count(200, "МГСУ") != 1 || tr.count(300, "МГСУ") != 2 {
		t.Fatal("successful recipient was retried or favorites missing on retry")
	}
	prepareVisitor(t, b, 100)
	_ = serviceLead(t, b, 0)
	before := len(tr.calls)
	b.sendVisitorAlerts(context.Background(), time.Now())
	if len(tr.calls) != before {
		t.Fatal("existing application triggered a visitor alert")
	}
	prepareVisitor(t, b, 200)
	b.sendVisitorAlerts(context.Background(), time.Now())
	if len(tr.calls) != before {
		t.Fatal("administrator was included in visitor alarms")
	}
}
