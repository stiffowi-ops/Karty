package telegram

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func moscowTime(value string) time.Time {
	at, err := time.ParseInLocation("2006-01-02 15:04:05", value, visitTZ)
	if err != nil {
		panic(err)
	}
	return at
}
func TestAdminWorkHoursAndWorkingDuration(t *testing.T) {
	for _, tc := range []struct {
		at   string
		want bool
	}{
		{"2026-11-17 08:59:59", false}, {"2026-11-17 09:00:00", true},
		{"2026-11-17 18:59:59", true}, {"2026-11-17 19:00:00", false},
		{"2026-11-17 23:59:59", false}, {"2026-11-21 10:00:00", true},
	} {
		if got := adminWorkTime(moscowTime(tc.at).UTC()); got != tc.want {
			t.Errorf("window %s: %v", tc.at, got)
		}
	}
	for _, tc := range []struct {
		from, to string
		hours    time.Duration
	}{
		{"2026-11-17 18:00:00", "2026-11-18 10:00:00", 2},
		{"2026-11-17 18:00:00", "2026-11-18 12:00:00", 4},
		{"2026-11-17 20:00:00", "2026-11-18 11:00:00", 2},
		{"2026-11-17 08:00:00", "2026-11-17 11:00:00", 2},
		{"2026-11-17 19:00:00", "2026-11-18 09:00:00", 0},
		{"2026-11-17 09:00:00", "2026-11-20 19:00:00", 40},
		{"2026-11-20 18:00:00", "2026-11-21 10:00:00", 2},
		{"2026-11-18 10:00:00", "2026-11-17 18:00:00", 0},
	} {
		got := adminWorkingDuration(moscowTime(tc.from).UTC(), moscowTime(tc.to))
		if got != tc.hours*time.Hour {
			t.Errorf("%s → %s: got %v, want %v", tc.from, tc.to, got, tc.hours*time.Hour)
		}
	}
	if adminWorkingDuration(time.Time{}, time.Now()) != 0 {
		t.Fatal("unknown creation time triggered an overdue reminder")
	}
}

func datedServiceLead(t *testing.T, b *Bot, owner int64, at time.Time) leadApplication {
	t.Helper()
	l := serviceLead(t, b, owner)
	l, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) { v.CreatedAt = at; return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func TestPendingApplicationRemindersUseWorkingHoursAndStopOnClaim(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := datedServiceLead(t, b, 0, moscowTime("2026-11-17 18:00:00"))
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	for _, at := range []string{"2026-11-17 19:00:00", "2026-11-17 22:00:00", "2026-11-18 08:59:59", "2026-11-18 09:59:00"} {
		b.notifyCaseEvents(ctx, moscowTime(at))
	}
	if len(tr.calls) != 0 {
		t.Fatal("overnight time counted or a reminder was sent outside office hours")
	}
	for i := 0; i < 2; i++ {
		b.notifyCaseEvents(ctx, moscowTime("2026-11-18 10:00:00"))
	}
	for _, id := range []int64{200, 300} {
		if tr.count(id, "⏰ Заявка") != 1 {
			t.Fatal("2 working hour reminder missing or repeated")
		}
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 12:00:00"))
	for _, id := range []int64{200, 300} {
		if tr.count(id, "‼️ Заявка") != 1 {
			t.Fatal("4 working hour escalation missing")
		}
	}
	if _, _, err := b.leads.takeInWork(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-19 10:00:00"))
	for _, id := range []int64{200, 300} {
		if tr.count(id, "⏰ Заявка") != 1 || tr.count(id, "‼️ Заявка") != 1 {
			t.Fatal("claimed application kept reminding administrators")
		}
	}
}

func TestSupplementRemindersPauseAtNightButInitialMessagesAreImmediate(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := datedServiceLead(t, b, 200, moscowTime("2026-11-17 18:00:00"))
	l = sendStoredSupplement(t, b, l.Number, nil, "Уточните условия")
	_, _, err := b.leads.mutateCase(l.Number, func(v *leadApplication) (bool, error) {
		v.Updates[0].CreatedAt = moscowTime("2026-11-17 18:00:00")
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	b.notifyCaseEvents(ctx, moscowTime("2026-11-17 22:00:00"))
	if tr.count(200, "🔔 Дополнение") != 1 || tr.count(200, "⏰ Дополнение") != 0 {
		t.Fatal("initial event was delayed, or a reminder was sent at night")
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 09:59:00"))
	if tr.count(200, "⏰ Дополнение") != 0 {
		t.Fatal("night hours counted toward supplement delay")
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 10:00:00"))
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 12:00:00"))
	if tr.count(200, "⏰ Дополнение") != 1 || tr.count(300, "‼️ Дополнение") != 1 {
		t.Fatal("supplement working-hour schedule or escalation routing is wrong")
	}
	if _, _, err := b.leads.acceptUpdate(l.Number, 1, 200); err != nil {
		t.Fatal(err)
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-19 12:00:00"))
	if tr.count(200, "⏰ Дополнение") != 1 || tr.count(300, "‼️ Дополнение") != 1 {
		t.Fatal("accepted supplement kept reminding")
	}
}

func TestOverdueRestartSendsOnlyLatestApplicationStageAndKeepsReceipts(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	_ = datedServiceLead(t, b, 0, moscowTime("2026-11-17 09:00:00"))
	restored, err := openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	b.leads = restored
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	b.notifyCaseEvents(ctx, moscowTime("2026-11-17 22:00:00"))
	if len(tr.calls) != 0 {
		t.Fatal("overdue reminders were sent at night after restart")
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 09:00:00"))
	if len(tr.calls) != 2 || tr.count(200, "⏰ Заявка") != 0 || tr.count(200, "‼️ Заявка") != 1 {
		t.Fatal("catch-up delivered both old stages instead of one escalation")
	}
	restored, err = openLeadStore(b.leads.path)
	if err != nil {
		t.Fatal(err)
	}
	b.leads = restored
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 10:00:00"))
	if len(tr.calls) != 2 {
		t.Fatal("delivered reminders repeated after restart")
	}
}

func TestFailedReminderWaitsForOpeningAndRetriesOnlyFailedRecipient(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	_ = datedServiceLead(t, b, 0, moscowTime("2026-11-17 16:59:00"))
	tr := &caseTransport{failAdmin: 300, failOnce: true}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	b.notifyCaseEvents(ctx, moscowTime("2026-11-17 18:59:00"))
	b.notifyCaseEvents(ctx, moscowTime("2026-11-17 19:00:00"))
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 08:59:00"))
	if len(tr.calls) != 2 {
		t.Fatal("failed reminder was retried outside office hours")
	}
	b.notifyCaseEvents(ctx, moscowTime("2026-11-18 09:00:00"))
	if tr.count(200, "⏰ Заявка") != 1 || tr.count(300, "⏰ Заявка") != 2 {
		t.Fatal("successful recipient was duplicated, or failed delivery was lost")
	}
}

func TestDeliveryRechecksClosingTimeAndCurrentApplicationState(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	l := datedServiceLead(t, b, 0, moscowTime("2026-11-17 09:00:00"))
	tr := &caseTransport{}
	b.http = &http.Client{Transport: tr}
	ctx := context.Background()
	key := fmt.Sprintf("case:reminder:%d", 200)
	clock := func() time.Time { return moscowTime("2026-11-17 19:00:00") }
	b.deliverCaseNotice(ctx, l, key, 200, "Напоминание", adminLeadMarkup(l), clock)
	if len(tr.calls) != 0 {
		t.Fatal("reminder batch continued sending past 19:00")
	}
	if _, _, err := b.leads.takeInWork(l.Number, 200); err != nil {
		t.Fatal(err)
	}
	clock = func() time.Time { return moscowTime("2026-11-18 09:00:00") }
	b.deliverCaseNotice(ctx, l, key, 200, "Напоминание", adminLeadMarkup(l), clock)
	if len(tr.calls) != 0 {
		t.Fatal("stale timer notified after administrator claimed the case")
	}
}
