package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func serviceLead(t *testing.T, b *Bot, owner int64) leadApplication {
	t.Helper()
	d := leadDraft{Kind: "existing", Name: "Клиент <Иван>", Phone: "+79991112233", LocationID: "001", Source: "card", Stage: "review"}
	if err := b.leads.putDraft(100, d); err != nil {
		t.Fatal(err)
	}
	l, _, err := b.leads.submit(100, "client100", "Метро Троицк")
	if err != nil {
		t.Fatal(err)
	}
	if owner > 0 {
		l, _, err = b.leads.takeInWork(l.Number, owner)
		if err != nil {
			t.Fatal(err)
		}
	}
	return l
}
func sendStoredSupplement(t *testing.T, b *Bot, n int64, ids []string, comment string) leadApplication {
	t.Helper()
	d, err := newSupplement(n)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		loc, ok := b.store.Find(id)
		if !ok {
			t.Fatal(id)
		}
		d.Locations = append(d.Locations, leadLocation{ID: id, Name: loc.Title})
	}
	d.Comment = comment
	d.Stage = "review"
	if err := b.leads.putSupplement(100, d); err != nil {
		t.Fatal(err)
	}
	l, changed, err := b.leads.submitSupplement(100, n, d.Token)
	if err != nil || !changed {
		t.Fatalf("submit update: %v %v", changed, err)
	}
	return l
}
func TestSupplementAtomicIdempotencyDeepCopiesAndRestart(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	d, _ := newSupplement(l.Number)
	d.Stage = "review"
	d.Comment = "Обсудить обе точки"
	d.Locations = []leadLocation{{ID: "002", Name: "Ашан Марфино"}, {ID: "001", Name: "Троицк"}}
	if err := b.leads.putSupplement(100, d); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := b.leads.submitSupplement(100, l.Number, d.Token)
			if err != nil {
				t.Error(err)
			}
			if changed {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	got, _ := b.leads.byNumber(l.Number)
	if created != 1 || len(got.Updates) != 1 || len(got.locations()) != 2 || len(got.Updates[0].Locations) != 1 {
		t.Fatalf("duplicates: created=%d %+v", created, got)
	}
	got.Updates[0].Locations[0].Name = "Changed"
	got.Locations[0].Name = "Changed"
	again, _ := b.leads.byNumber(l.Number)
	if again.Updates[0].Locations[0].Name == "Changed" || again.Locations[0].Name == "Changed" {
		t.Fatal("store returned shared slices")
	}
	restored, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := restored.byNumber(l.Number)
	if len(after.Updates) != 1 || len(after.locations()) != 2 || after.Updates[0].Comment != d.Comment {
		t.Fatalf("restart lost update: %+v", after)
	}
	if _, exists := restored.supplement(100); exists {
		t.Fatal("sent draft survived")
	}
	if _, _, err := restored.submitSupplement(999, l.Number, d.Token); err == nil {
		t.Fatal("other client submitted update")
	}
}
func TestManualCompletionRequiresAcceptedUpdatesAndRetainsHistory(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	l = sendStoredSupplement(t, b, l.Number, []string{"002"}, "Первое уточнение")
	if _, _, err := b.leads.resolveCase(l.Number, 200); err == nil {
		t.Fatal("resolved with unread update")
	}
	if _, changed, err := b.leads.acceptUpdate(l.Number, 1, 200); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if _, changed, err := b.leads.acceptUpdate(l.Number, 1, 300); err != nil || changed {
		t.Fatal("second acknowledgment was not idempotent", err)
	}
	l = sendStoredSupplement(t, b, l.Number, nil, "Ещё один вопрос")
	if _, _, err := b.leads.resolveCase(l.Number, 200); err == nil {
		t.Fatal("stale completion ignored a new update")
	}
	if _, _, err := b.leads.acceptUpdate(l.Number, 2, 200); err != nil {
		t.Fatal(err)
	}
	resolved, changed, err := b.leads.resolveCase(l.Number, 200)
	if err != nil || !changed || !resolved.isResolved() {
		t.Fatal(changed, err)
	}
	if _, changed, err := b.leads.resolveCase(l.Number, 300); err != nil || changed {
		t.Fatal("repeat completion changed result", err)
	}
	if err := b.leads.putSupplement(100, supplementDraft{Number: l.Number, Token: "late", Stage: "review", Comment: "Поздно"}); err == nil {
		t.Fatal("resolved case accepted update")
	}
	if err := b.startLead(context.Background(), 100, 100, "card", "003"); err != nil {
		t.Fatal(err)
	}
	d, ok := b.leads.draft(100)
	if !ok || d.Stage != "reuse" || d.Phone != "+79991112233" {
		t.Fatalf("contacts not offered for confirmation: %+v", d)
	}
	if err := b.handleLeadCallback(context.Background(), 100, user{ID: 100}, "lead:reuse"); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(context.Background(), 100, user{ID: 100}, "lead:confirm"); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(context.Background(), 100, user{ID: 100}, "lead:submit"); err != nil {
		t.Fatal(err)
	}
	old, _ := b.leads.byNumber(1)
	current, _ := b.leads.submitted(100)
	if len(b.leads.allSubmitted()) != 2 || !old.isResolved() || len(old.Updates) != 2 || current.Number != 2 || current.isResolved() {
		t.Fatal("new application overwrote resolved history")
	}
}
func TestResolveAndSupplementRaceCannotLoseTheUpdate(t *testing.T) {
	for attempt := 0; attempt < 8; attempt++ {
		b, _ := makeLeadTestBot(t)
		l := serviceLead(t, b, 200)
		d, _ := newSupplement(l.Number)
		d.Stage = "review"
		d.Comment = "Уточнение одновременно с завершением"
		if err := b.leads.putSupplement(100, d); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _, _ = b.leads.submitSupplement(100, l.Number, d.Token) }()
		go func() { defer wg.Done(); _, _, _ = b.leads.resolveCase(l.Number, 200) }()
		wg.Wait()
		got, _ := b.leads.byNumber(l.Number)
		if got.isResolved() && len(got.Updates) > 0 {
			t.Fatal("new update was silently closed")
		}
		if !got.isResolved() && len(got.Updates) != 1 {
			t.Fatal("neither completion nor update survived")
		}
	}
}
func TestCabinetGroupedSupplementAndOwnership(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	l := serviceLead(t, b, 0)
	ctx := context.Background()
	u := user{ID: 100}
	if err := b.beginSupplement(ctx, 100, 100, l.Number, "add", ""); err != nil {
		t.Fatal(err)
	}
	d, _ := b.leads.supplement(100)
	for _, id := range []string{"002", "003"} {
		if err := b.handleCabinetCallback(ctx, 100, u, supplementData("pick", d, id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.handleCabinetCallback(ctx, 100, u, supplementData("note", d, "")); err != nil {
		t.Fatal(err)
	}
	if handled, err := b.handleSupplementText(ctx, 100, u, "Интересуют обе точки <рядом>"); !handled || err != nil {
		t.Fatal(handled, err)
	}
	if err := b.handleCabinetCallback(ctx, 100, u, supplementData("send", d, "")); err != nil {
		t.Fatal(err)
	}
	if err := b.handleCabinetCallback(ctx, 100, u, supplementData("send", d, "")); err != nil {
		t.Fatal(err)
	}
	got, _ := b.leads.byNumber(l.Number)
	if len(got.Updates) != 1 || len(got.locations()) != 3 {
		t.Fatalf("grouped update failed %+v", got)
	}
	b.processLeadDeliveries(ctx)
	alerts := 0
	for _, p := range tr.payloads {
		if text, _ := p["text"].(string); strings.Contains(text, "🔔 Дополнение к заявке") {
			alerts++
			if !strings.Contains(text, "&lt;рядом&gt;") {
				t.Fatal("comment not escaped")
			}
		}
	}
	if alerts != 2 {
		t.Fatalf("expected one grouped alert per administrator, got %d", alerts)
	}
	before := len(tr.payloads)
	if err := b.handleCabinetCallback(ctx, 999, user{ID: 999}, fmt.Sprintf("cab:view:%d", l.Number)); err != nil {
		t.Fatal(err)
	}
	for _, p := range tr.payloads[before:] {
		if text, _ := p["text"].(string); strings.Contains(text, "79991112233") || strings.Contains(text, "Троицк") {
			t.Fatal("another user's case was disclosed")
		}
	}
	if err := b.handle(ctx, update{Callback: &callbackQuery{ID: "forged", Data: "adm:case:ack:1:1", From: user{ID: 999}, Message: &message{Chat: chat{ID: 999, Type: "private"}}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = b.leads.byNumber(1)
	if got.Updates[0].AcceptedAt != nil {
		t.Fatal("non-admin accepted update")
	}
}
func TestPausedFormsDoNotConsumeSearchOrOverwriteClientData(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	ctx := context.Background()
	u := user{ID: 100}
	if err := b.startLead(ctx, 100, 100, "menu", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(ctx, 100, u, "lead:kind:existing"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"/start", "Троицк"} {
		if err := b.handle(ctx, update{Message: &message{Chat: chat{ID: 100, Type: "private"}, From: u, Text: text}}); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := b.leads.draft(100)
	if !d.Paused || d.Name != "" || d.Stage != "name" {
		t.Fatalf("search overwrote draft %+v", d)
	}
	if _, ok := tr.card["rich_message"]; !ok {
		t.Fatal("paused form blocked location search")
	}
	if err := b.handleLeadCallback(ctx, 100, u, "lead:resume"); err != nil {
		t.Fatal(err)
	}
	if handled, err := b.handleLeadText(ctx, 100, u, "Иван"); !handled || err != nil {
		t.Fatal(handled, err)
	}
	restored, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := restored.draft(100)
	if !after.Paused || after.Name != "Иван" {
		t.Fatal("restart did not pause and preserve draft")
	}
}

type caseTransport struct {
	mu        sync.Mutex
	failAdmin int64
	failOnce  bool
	calls     []map[string]any
}

func (tr *caseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var p map[string]any
	_ = json.NewDecoder(r.Body).Decode(&p)
	tr.calls = append(tr.calls, p)
	body := fmt.Sprintf(`{"ok":true,"result":{"message_id":%d}}`, len(tr.calls))
	if p["chat_id"] == float64(tr.failAdmin) && tr.failOnce {
		tr.failOnce = false
		body = `{"ok":false,"description":"temporary delivery failure"}`
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}
func (tr *caseTransport) count(id int64, fragment string) int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	n := 0
	for _, p := range tr.calls {
		if p["chat_id"] == float64(id) {
			text, _ := p["text"].(string)
			if strings.Contains(text, fragment) {
				n++
			}
		}
	}
	return n
}
func TestSupplementRoutingRetriesRemindersAndLifecycleNotifications(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 0)
	l = sendStoredSupplement(t, b, l.Number, []string{"002"}, "Нужны подробности")
	tr := &caseTransport{failAdmin: 300, failOnce: true}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, visitTZ)
	if _, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) {
		v.CreatedAt = now
		v.Updates[0].CreatedAt = now
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	b.notifyCaseEvents(ctx, now)
	b.notifyCaseEvents(ctx, now)
	if tr.count(200, "🔔 Дополнение") != 1 || tr.count(300, "🔔 Дополнение") != 2 {
		t.Fatalf("per-recipient retry failed: %+v", tr.calls)
	}
	if _, _, err := b.leads.takeInWork(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	b.notifyCaseEvents(ctx, now)
	l = sendStoredSupplement(t, b, l.Number, []string{"003"}, "Второй вопрос")
	if _, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.Updates[1].CreatedAt = now; return true, nil }); err != nil {
		t.Fatal(err)
	}
	b.notifyCaseEvents(ctx, now)
	if tr.count(200, "🔔 Дополнение") != 2 || tr.count(300, "🔔 Дополнение") != 2 {
		t.Fatal("assigned update was not routed only to its owner")
	}
	b.notifyCaseEvents(ctx, now.Add(2*time.Hour+time.Minute))
	b.notifyCaseEvents(ctx, now.Add(2*time.Hour+time.Minute))
	if tr.count(200, "⏰ Дополнение") != 2 || tr.count(300, "⏰ Дополнение") != 0 {
		t.Fatal("owner reminders duplicated or misrouted")
	}
	b.notifyCaseEvents(ctx, now.Add(4*time.Hour+time.Minute))
	if tr.count(300, "‼️ Дополнение") != 2 {
		t.Fatal("unaccepted additions were not escalated")
	}
	for _, id := range []int{1, 2} {
		if _, _, err := b.leads.acceptUpdate(l.Number, id, 200); err != nil {
			t.Fatal(err)
		}
	}
	b.notifyCaseEvents(ctx, now)
	b.notifyCaseEvents(ctx, now)
	if tr.count(100, "взято в работу") != 1 || tr.count(100, "Специалист принял дополнение") != 2 {
		t.Fatal("client lifecycle notices missing or duplicated")
	}
	if _, _, err := b.leads.resolveCase(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	b.notifyCaseEvents(ctx, now)
	restored, err := openLeadStore(filepath.Join(b.store.DataDir(), "leads.json"))
	if err != nil {
		t.Fatal(err)
	}
	b.leads = restored
	b.notifyCaseEvents(ctx, now.Add(24*time.Hour))
	if tr.count(100, processedText) != 1 || tr.count(300, "‼️ Дополнение") != 2 {
		t.Fatal("restart repeated finished notifications or reminders")
	}
}
func TestAdminCompletesInOneClickWithoutAComment(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 0)
	ctx := context.Background()
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	click := func(id int64) {
		t.Helper()
		if err := b.handleCaseAdminCallback(ctx, user{ID: id}, "adm:case:resolve:1"); err != nil {
			t.Fatal(err)
		}
	}
	click(200)
	before, _ := b.leads.byNumber(l.Number)
	if before.isResolved() {
		t.Fatal("unassigned request closed")
	}
	if _, _, err := b.leads.takeInWork(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	click(999)
	before, _ = b.leads.byNumber(l.Number)
	if before.isResolved() {
		t.Fatal("unauthorized completion")
	}
	click(200)
	click(300)
	b.processLeadDeliveries(ctx)
	got, _ := b.leads.byNumber(l.Number)
	if !got.isResolved() || got.ResolvedBy != 200 || got.Resolution != "" {
		t.Fatalf("manual result: %+v", got)
	}
	b.notifyCaseEvents(ctx, time.Now())
	if tr.count(100, processedText) != 1 {
		t.Fatal("expected one completion notice")
	}
	for _, call := range tr.calls {
		if call["chat_id"] == float64(100) && strings.Contains(fmt.Sprint(call["text"]), processedText) && call["text"] != processedText {
			t.Fatal("unexpected text in completion notice", call["text"])
		}
	}
	if err := b.showClientArchive(ctx, 100, 100, 0, true); err != nil {
		t.Fatal(err)
	}
}

func TestUnsentSupplementDoesNotBlockTheNextApplication(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	d, _ := newSupplement(l.Number)
	d.Stage = "comment"
	d.Comment = "Черновик"
	if err := b.leads.putSupplement(100, d); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.leads.resolveCase(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.leads.supplement(100); ok {
		t.Fatal("old unsent draft survived completion")
	}
	if err := b.startLead(context.Background(), 100, 100, "menu", ""); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(context.Background(), 100, user{ID: 100}, "lead:fresh"); err != nil {
		t.Fatal(err)
	}
	if err := b.handleLeadCallback(context.Background(), 100, user{ID: 100}, "lead:kind:existing"); err != nil {
		t.Fatal(err)
	}
	if err := b.handle(context.Background(), update{Message: &message{Chat: chat{ID: 100, Type: "private"}, From: user{ID: 100}, Text: "Пётр"}}); err != nil {
		t.Fatal(err)
	}
	draft, _ := b.leads.draft(100)
	if draft.Name != "Пётр" {
		t.Fatal("old supplement blocked the new form")
	}
}

func TestEveryHistoryPageAndMaximumMessageSize(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	l = sendStoredSupplement(t, b, l.Number, nil, strings.Repeat("😀", 1000))
	for _, id := range []int{1} {
		if _, _, err := b.leads.acceptUpdate(l.Number, id, 200); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b.leads.resolveCase(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	// Previously completed cases can contain a long written result.
	l, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.Resolution = strings.Repeat("😀", 1000); return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	for page := 0; page < len(caseEvents(l))+2; page++ {
		if err := b.showCaseTimeline(context.Background(), 100, l, page, false); err != nil {
			t.Fatal(err)
		}
		text, _ := tr.card["text"].(string)
		if strings.Contains(text, "😀") && strings.Count(text, "😀") != 1000 {
			t.Fatal("history silently truncated the customer's text")
		}
	}
	// Use 20 long UTF-16 titles and maximum input to verify real Telegram limits.
	l.Locations = nil
	for i := 0; i < 20; i++ {
		l.Locations = append(l.Locations, leadLocation{ID: fmt.Sprint(i), Name: strings.Repeat("😀", 70)})
	}
	_, _, err = b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.Locations = l.Locations; return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := b.showClientCase(context.Background(), 100, 100, l.Number); err != nil {
		t.Fatal(err)
	}
	strip := regexp.MustCompile(`<[^>]*>`)
	for _, text := range []string{tr.card["text"].(string), adminLeadText(l)} {
		visible := html.UnescapeString(strip.ReplaceAllString(text, ""))
		if n := utf16Len(visible); n > 4096 {
			t.Fatalf("message exceeds Telegram limit: %d", n)
		}
	}
}

func TestLegacyCasesRemainInTheirOriginalState(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	_, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.ServiceVersion = 0; return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reloaded.submitted(100)
	if got.isResolved() || leadStatus(got) != "🛠 В работе" {
		t.Fatal("legacy assignment was silently resolved")
	}
}


func TestClosingWarningAt45MinutesOnlyOnceForOwner(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	l = sendStoredSupplement(t, b, l.Number, nil, "Нужно уточнить")
	taken := time.Now().UTC().Add(-45 * time.Minute)
	if _, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) {
		v.TakenAt = &taken
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	b.notifyCaseEvents(ctx, taken.Add(44*time.Minute))
	if tr.count(200, "скоро будет автоматически завершена") != 0 {
		t.Fatal("warning sent too early")
	}
	b.notifyCaseEvents(ctx, taken.Add(45*time.Minute))
	if tr.count(200, "скоро будет автоматически завершена") != 1 ||
		tr.count(300, "скоро будет автоматически завершена") != 0 ||
		tr.count(200, "Непринятых дополнений: 1") != 1 {
		t.Fatal("warning was not addressed to the owner or had wrong supplement count")
	}
	restored, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	b.leads = restored
	b.notifyCaseEvents(ctx, taken.Add(46*time.Minute))
	if tr.count(200, "скоро будет автоматически завершена") != 1 {
		t.Fatal("warning repeated after restart")
	}
	b.notifyCaseEvents(ctx, taken.Add(time.Hour))
	if tr.count(200, "скоро будет автоматически завершена") != 1 {
		t.Fatal("warning sent at or after deadline")
	}
}

func TestClientSupplementCounterAndChronologicalTimeline(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	if err := b.showCabinet(context.Background(), 100, 100); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(tr.card["text"]), "Дополнения: 0 из 2 использовано") {
		t.Fatal("missing initial supplement counter")
	}
	l = sendStoredSupplement(t, b, l.Number, nil, "Первое уточнение")
	if err := b.showClientCase(context.Background(), 100, 100, l.Number); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(tr.card["text"]), "Дополнения: 1 из 2 использовано") ||
		!strings.Contains(fmt.Sprint(tr.card["text"]), "ожидает принятия") {
		t.Fatal("missing active supplement counter or acceptance status")
	}
	if _, _, err := b.leads.acceptUpdate(l.Number, 1, 200); err != nil {
		t.Fatal(err)
	}
	l = sendStoredSupplement(t, b, l.Number, nil, "Второе уточнение")
	if err := b.showCabinet(context.Background(), 100, 100); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fmt.Sprint(tr.card["text"]), "Дополнения: 2 из 2 использовано") ||
		!strings.Contains(fmt.Sprint(tr.card["text"]), supplementLimitText) {
		t.Fatal("missing final limit and waiting notice")
	}
	for _, page := range []int{0, 1} {
		if err := b.showCaseTimeline(context.Background(), 100, l, page, false); err != nil {
			t.Fatal(err)
		}
		screen, _ := tr.card["text"].(string)
		if !strings.Contains(screen, "События по порядку") {
			t.Fatal("client timeline did not switch to chronological view")
		}
		if page == 0 && (!strings.Contains(screen, "Обращение получено") || !strings.Contains(screen, "Специалист взял обращение в работу")) {
			t.Fatal("first page does not start from receipt and assignment")
		}
	}
}
