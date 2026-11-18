package telegram

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"locationbot/internal/adminbackup"
)

func prepareCleanupLead(t *testing.T, b *Bot, id int64) int64 {
	t.Helper()
	d := leadDraft{Kind: "existing", Name: "Клиент", Phone: "+79991112233", LocationID: "001", Stage: "review", Source: "menu"}
	if err := b.leads.putDraft(id, d); err != nil {
		t.Fatal(err)
	}
	lead, already, err := b.leads.submit(id, "user", "Троицк")
	if err != nil || already {
		t.Fatalf("submission: %+v %v %v", lead, already, err)
	}
	return lead.Number
}
func TestLeadSoftDeleteRestoreAndPermanentErase(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	first := prepareCleanupLead(t, b, 101)
	second := prepareCleanupLead(t, b, 102)
	if err := b.leads.setHidden([]int64{first}, true); err == nil {
		t.Fatal("pending application should not be hidden")
	}
	for _, n := range []int64{first, second} {
		if _, _, err := b.leads.takeInWork(n, 200); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.leads.resolveCase(n, 200); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.leads.setHidden([]int64{first, second}, true); err != nil {
		t.Fatal(err)
	}
	if len(b.leads.visibleSubmitted()) != 0 || len(b.leads.allSubmitted()) != 2 {
		t.Fatal("soft deletion erased pending or history")
	}
	if err := b.leads.setHidden([]int64{first}, false); err != nil {
		t.Fatal(err)
	}
	if len(b.leads.visibleSubmitted()) != 1 {
		t.Fatal("restore failed")
	}
	if err := b.leads.setHidden([]int64{first}, true); err != nil {
		t.Fatal(err)
	}
	root := b.store.DataDir()
	if err := adminbackup.MarkDeleted(root, []int64{first, second}, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.leads.eraseHidden([]int64{first, second}); err != nil {
		t.Fatal(err)
	}
	if len(b.leads.allSubmitted()) != 0 {
		t.Fatal("permanent lead removal failed")
	}
	ledger, err := adminbackup.ReadDeletionLedger(root)
	if err != nil || len(ledger.Leads) != 2 {
		t.Fatalf("markers absent %+v %v", ledger, err)
	}
	reloaded, err := openLeadStore(filepath.Join(root, "leads.json"))
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.state.NextID != 2 {
		t.Fatalf("application counter rolled back: %d", reloaded.state.NextID)
	}
	next := prepareCleanupLead(t, b, 103)
	if next != 3 {
		t.Fatalf("new application number reused %d", next)
	}
}

func TestVisitorCleanupNoRepeatAlarmAfterPermanentDelete(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	v, err := openVisitorStore(filepath.Join(b.store.DataDir(), "visitors.json"))
	if err != nil {
		t.Fatal(err)
	}
	b.visitors = v
	userID := int64(777)
	when := time.Now().UTC().Add(-2 * time.Hour)
	if err := v.record(user{ID: userID, FirstName: "Посетитель"}, "Главное меню", true, when); err != nil {
		t.Fatal(err)
	}
	if err := v.setHidden([]int64{userID}, true); err != nil {
		t.Fatal(err)
	}
	if len(v.due(time.Now())) != 0 {
		t.Fatal("hidden visitor still triggers alarm")
	}
	if err := v.setHidden([]int64{userID}, false); err != nil {
		t.Fatal(err)
	}
	if len(v.due(time.Now())) != 1 {
		t.Fatal("restored visitor not active")
	}
	if err := v.setHidden([]int64{userID}, true); err != nil {
		t.Fatal(err)
	}
	if err := adminbackup.MarkDeleted(b.store.DataDir(), nil, []int64{userID}); err != nil {
		t.Fatal(err)
	}
	if err := v.eraseHidden([]int64{userID}); err != nil {
		t.Fatal(err)
	}
	b.observeVisitor(user{ID: userID}, "Главное меню", true)
	if _, found := v.find(userID); found {
		t.Fatal("permanently deleted visitor recreated")
	}
	if len(v.due(time.Now())) != 0 {
		t.Fatal("deleted visitor re-alerted")
	}
}

func TestAdminCleanupSelectionAndConfirmation(t *testing.T) {
	b, tr := makeLeadTestBot(t)
	n := prepareCleanupLead(t, b, 101)
	if _, _, err := b.leads.takeInWork(n, 200); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.leads.resolveCase(n, 200); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := b.beginCleanup(ctx, 200, 200, "l", false); err != nil {
		t.Fatal(err)
	}
	if handled, err := b.handleAdminCleanup(ctx, 200, 200, "adm:clean:toggle:1"); !handled || err != nil {
		t.Fatalf("toggle %v %v", handled, err)
	}
	s := b.adminSession(200)
	if !s.CleanupSelected[n] {
		t.Fatal("record not selected")
	}
	if _, err := b.handleAdminCleanup(ctx, 200, 200, "adm:clean:ask:h"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.handleAdminCleanup(ctx, 200, 200, "adm:clean:apply:h"); err != nil {
		t.Fatal(err)
	}
	if len(b.leads.visibleSubmitted()) != 0 {
		t.Fatal("hide was not applied")
	}
	if err := b.beginCleanup(ctx, 200, 200, "l", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.handleAdminCleanup(ctx, 200, 200, "adm:clean:all"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.handleAdminCleanup(ctx, 200, 200, "adm:clean:ask:r"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.handleAdminCleanup(ctx, 200, 200, "adm:clean:apply:r"); err != nil {
		t.Fatal(err)
	}
	if len(b.leads.visibleSubmitted()) != 1 {
		t.Fatal("restore did not work")
	}
	_ = tr
	if err := b.handleAdminCallback(ctx, 101, user{ID: 101}, "adm:clean:start:l:a"); err != nil {
		t.Fatal(err)
	}
	if len(b.adminSession(101).CleanupSelected) != 0 {
		t.Fatal("non-admin acquired selection")
	}
}

func TestVisitorCleanupPageAndSingleItem(t *testing.T) {
	b, _ := makeLeadTestBot(t)
	v, err := openVisitorStore(filepath.Join(b.store.DataDir(), "visitors.json"))
	if err != nil {
		t.Fatal(err)
	}
	b.visitors = v
	for i := 1; i <= 12; i++ {
		if err := v.record(user{ID: int64(i), FirstName: "Тест"}, "Главное меню", true, time.Now().Add(-time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.beginCleanup(context.Background(), 200, 200, "v", false); err != nil {
		t.Fatal(err)
	}
	s := b.adminSession(200)
	if _, err := b.handleAdminCleanup(context.Background(), 200, 200, "adm:clean:all"); err != nil {
		t.Fatal(err)
	}
	if len(s.CleanupSelected) != cleanupPageSize {
		t.Fatalf("expected page selection, got %d", len(s.CleanupSelected))
	}
	if _, err := b.handleAdminCleanup(context.Background(), 200, 200, "adm:clean:page:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.handleAdminCleanup(context.Background(), 200, 200, "adm:clean:all"); err != nil {
		t.Fatal(err)
	}
	if len(s.CleanupSelected) != 12 {
		t.Fatalf("expected all page selections, got %d", len(s.CleanupSelected))
	}
	if _, err := b.handleAdminCleanup(context.Background(), 200, 200, "adm:clean:ask:h"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s.CleanupAction, "h") {
		t.Fatal("missing confirmed selection state")
	}
	if _, err := b.handleAdminCleanup(context.Background(), 200, 200, "adm:clean:apply:h"); err != nil {
		t.Fatal(err)
	}
	for _, p := range v.snapshot() {
		if !p.Hidden {
			t.Fatal("bulk hide skipped a visitor")
		}
	}
}
