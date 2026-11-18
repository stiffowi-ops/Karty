package telegram

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTwoSendsPerCaseAcrossAssignmentAndRestart(t *testing.T) {
	b, transport := makeLeadTestBot(t)
	ctx := context.Background()
	l := serviceLead(t, b, 0)
	d, _ := newSupplement(l.Number)
	if err := b.leads.putSupplement(100, d); err != nil {
		t.Fatal(err)
	}
	if err := b.leads.cancelSupplement(100, d.Token); err != nil {
		t.Fatal(err)
	}
	l = sendStoredSupplement(t, b, l.Number, []string{"002", "003"}, "Обе локации")
	if _, _, err := b.leads.takeInWork(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.leads.acceptUpdate(l.Number, 1, 200); err != nil {
		t.Fatal(err)
	}
	d, _ = newSupplement(l.Number)
	d.Stage, d.Comment = "review", "Второй вопрос"
	if err := b.leads.putSupplement(100, d); err != nil {
		t.Fatal(err)
	}
	click := func(data string) {
		t.Helper()
		if err := b.handleCabinetCallback(ctx, 100, user{ID: 100}, data); err != nil {
			t.Fatal(err)
		}
	}
	waitMessage := func() {
		t.Helper()
		if !strings.Contains(fmt.Sprint(transport.card["text"]), supplementLimitText) {
			t.Fatalf("missing waiting message: %v", transport.card["text"])
		}
	}
	click(supplementData("send", d, ""))
	waitMessage()
	reopened, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	b.leads = reopened
	for _, action := range []string{supplementData("send", d, ""), "cab:add:1:003", "cab:comment:1", "cab:choose:1:old", "cab:send:1:old"} {
		click(action)
		waitMessage()
	}
	if err := b.openLead(ctx, 100, 100, "card", "003"); err != nil {
		t.Fatal(err)
	}
	waitMessage()
	if data := b.homeLeadAction(100)["callback_data"]; data != "cab:view:1" {
		t.Fatal("main menu action", data)
	}
	click("cab:view:1")
	waitMessage()
	if strings.Contains(fmt.Sprint(transport.card["reply_markup"]), "cab:add:") {
		t.Fatal("full request still offers an addition")
	}
	if err := b.leads.putSupplement(100, supplementDraft{Number: 1, Token: "third", Stage: "review", Comment: "later"}); err == nil {
		t.Fatal("third draft accepted")
	}
	got, _ := b.leads.submitted(100)
	if len(got.Updates) != 2 || len(got.Updates[0].Locations) != 2 {
		t.Fatalf("wrong number of additions: %+v", got.Updates)
	}
	// Preserve all updates already recorded by a release without a limit.
	if _, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) {
		v.Updates = append(v.Updates, leadUpdate{ID: 3, Token: "old-3", Comment: "Историческое уточнение"})
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	click("cab:add:1")
	waitMessage()
	for _, id := range []int{2, 3} {
		if _, _, err := b.leads.acceptUpdate(1, id, 200); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := b.leads.resolveCase(1, 200); err != nil {
		t.Fatal(err)
	}
	next := serviceLead(t, b, 0)
	next = sendStoredSupplement(t, b, next.Number, nil, "Новая заявка")
	old, _ := b.leads.byNumber(1)
	if next.Number == 1 || len(next.Updates) != 1 || len(old.Updates) != 3 {
		t.Fatal("limit should apply independently to each request")
	}
}

func TestCaseExpiryAtOneElapsedHourAndCatchUpAfterRestart(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	l = sendStoredSupplement(t, b, l.Number, nil, "Ещё не принято")
	draft, _ := newSupplement(l.Number)
	draft.Stage, draft.Comment = "review", "Ещё не отправлено"
	if err := b.leads.putSupplement(100, draft); err != nil {
		t.Fatal(err)
	}
	taken := time.Date(2025, 2, 21, 23, 45, 0, 0, visitTZ)
	if _, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.TakenAt = &taken; return true, nil }); err != nil {
		t.Fatal(err)
	}
	repeat, changed, err := b.leads.takeInWork(l.Number, 300)
	if err != nil || changed || !repeat.TakenAt.Equal(taken) || repeat.TakenBy != 200 {
		t.Fatal("reassignment reset original timer", err)
	}
	due := taken.Add(time.Hour) // Saturday 00:45 Moscow time.
	closed, earliest, err := b.leads.expireCases(due.Add(-time.Nanosecond))
	if err != nil || len(closed) != 0 || !earliest.Equal(due) {
		t.Fatal("early completion or wrong deadline", closed, earliest, err)
	}
	if _, _, err := b.leads.submitSupplement(100, l.Number, draft.Token); err == nil {
		t.Fatal("accepted supplement after deadline before sweep")
	}
	restored, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	b.leads = restored
	b.expireDueCases()
	got, _ := b.leads.byNumber(l.Number)
	if !got.isResolved() || !got.ResolvedAt.Equal(due) || got.ResolvedBy != 0 || got.Resolution != "" || len(got.Updates) != 1 || got.Updates[0].AcceptedAt != nil {
		t.Fatalf("incorrect catch-up: %+v", got)
	}
	if _, exists := b.leads.supplement(100); exists {
		t.Fatal("expired draft survived")
	}
	if buttons := fmt.Sprint(supplementAdminMarkup(got, got.Updates[0])); strings.Contains(buttons, "Дополнение принято") || strings.Contains(buttons, "Принять дополнение") {
		t.Fatal("closed unread addition has incorrect controls", buttons)
	}
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	b.processLeadDeliveries(context.Background())
	if _, changed, err := b.leads.resolveCase(l.Number, 200); err != nil || changed {
		t.Fatal("manual click changed auto result", err)
	}
	restored, err = openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	b.leads = restored
	closed, _, err = b.leads.expireCases(due.Add(24 * time.Hour))
	b.notifyCaseEvents(context.Background(), due.Add(24*time.Hour))
	if err != nil || len(closed) != 0 || tr.count(100, processedText) != 1 || tr.count(100, "взято в работу") != 0 || tr.count(100, "принял дополнение") != 0 || tr.count(200, "🔔 Дополнение") != 1 {
		t.Fatal("duplicate or misleading notices on restart", err, tr.calls)
	}
}

func TestCaseExpiryBoundariesAndEligibility(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	already := past.Add(time.Minute)
	var zero time.Time
	s := b.leads
	s.mu.Lock()
	next := s.cloneLocked()
	next.Submitted = map[string]leadApplication{
		"1": {UserID: 1, Number: 1, TakenInWork: true, TakenAt: &past},
		"2": {UserID: 2, Number: 2, CreatedAt: past},
		"3": {UserID: 3, Number: 3, TakenInWork: true},
		"4": {UserID: 4, Number: 4, TakenInWork: true, TakenAt: &past, Hidden: true},
		"5": {UserID: 5, Number: 5, TakenInWork: true, TakenAt: &past, ResolvedAt: &already, ResolvedBy: 200},
		"6": {UserID: 6, Number: 6, TakenInWork: true, TakenAt: &zero},
	}
	next.History = []leadApplication{
		{UserID: 1, Number: 7, TakenInWork: true, TakenAt: &past, ServiceVersion: 1},
		{UserID: 1, Number: 8, TakenInWork: true, TakenAt: &past, ServiceVersion: 2},
	}
	err := s.saveLocked(next)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	closed, _, err := s.expireCases(now)
	if err != nil || len(closed) != 2 {
		t.Fatal(closed, err)
	}
	for _, id := range []int64{1, 8} {
		v, _ := s.byNumber(id)
		if v.ResolvedAt == nil || !v.ResolvedAt.Equal(now) {
			t.Fatal("missed exact boundary", id)
		}
	}
	for _, id := range []int64{2, 3, 4, 6, 7} {
		v, _ := s.byNumber(id)
		if v.isResolved() {
			t.Fatal("unexpected closure", id)
		}
	}
	v, _ := s.byNumber(5)
	if v.ResolvedBy != 200 || !v.ResolvedAt.Equal(already) {
		t.Fatal("overwrote manual completion")
	}
}

func TestExpiryWorkerWakesAtDeadline(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := serviceLead(t, b, 200)
	b.deliveryWake = make(chan struct{}, 1)
	taken := time.Now().Add(-time.Hour + 200*time.Millisecond)
	if _, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.TakenAt = &taken; return true, nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	earliest := b.expireDueCases()
	go func() { defer close(done); b.runCaseExpiry(ctx, earliest) }()
	defer func() { cancel(); <-done }()
	select {
	case <-b.deliveryWake:
		v, _ := b.leads.byNumber(l.Number)
		if v.ResolvedAt == nil || !v.ResolvedAt.Equal(taken.Add(time.Hour)) {
			t.Fatal("worker queued an uncommitted completion")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker missed the deadline")
	}
}
