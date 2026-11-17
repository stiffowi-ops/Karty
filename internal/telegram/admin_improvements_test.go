package telegram

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"locationbot/internal/analytics"
	"locationbot/internal/catalog"
)

func lastAdminMessage(t *testing.T, tr *adminTransport) (string, string) {
	t.Helper()
	for i := len(tr.payloads) - 1; i >= 0; i-- {
		p := tr.payloads[i]
		if msg, ok := p["text"].(string); ok {
			raw, _ := json.Marshal(p["reply_markup"])
			return msg, string(raw)
		}
	}
	t.Fatal("no Telegram message recorded")
	return "", ""
}
func TestAdminDashboardAndRankingJumpToLocation(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	ctx := context.Background()
	b.recordLocationAnalytics(777, "020", analytics.View)
	b.recordLocationAnalytics(777, "020", analytics.FavoriteAdded)
	if err := b.showAdminMenu(ctx, 100); err != nil {
		t.Fatal(err)
	}
	msg, kb := lastAdminMessage(t, tr)
	for _, want := range []string{"Локаций", "Просмотров за 7 дней", "Сейчас в избранном", "Заявки", "Система"} {
		if !strings.Contains(msg+kb, want) {
			t.Fatalf("missing %q: %s %s", want, msg, kb)
		}
	}
	if err := b.showAdminStats(ctx, 100, "views", 30); err != nil {
		t.Fatal(err)
	}
	_, kb = lastAdminMessage(t, tr)
	if !strings.Contains(kb, "adm:insight:020:30") {
		t.Fatalf("ranking card not clickable: %s", kb)
	}
	if err := b.showLocationInsights(ctx, 100, "020", 30); err != nil {
		t.Fatal(err)
	}
	msg, kb = lastAdminMessage(t, tr)
	if !strings.Contains(msg, "Уникальных") || !strings.Contains(msg, "Открытий") || !strings.Contains(kb, "adm:edit:020") || !strings.Contains(kb, "adm:histloc:020") {
		t.Fatalf("location detail incomplete: %s %s", msg, kb)
	}
	if err := b.showAdminStats(ctx, 100, "saved", 7); err != nil {
		t.Fatal(err)
	}
	msg, kb = lastAdminMessage(t, tr)
	if !strings.Contains(msg, "На текущий момент") || strings.Contains(kb, "adm:stats:saved:7") || strings.Contains(kb, "adm:stats:saved:30") {
		t.Fatalf("saved is a snapshot, must not be filtered by date: %s %s", msg, kb)
	}
}
func TestRemoveSectionActuallyRemovesAndCanBeRestoredFromHistory(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	ctx := context.Background()
	if err := b.startAdminEditor(ctx, 100, 100, "020"); err != nil {
		t.Fatal(err)
	}
	sess := b.adminSession(100)
	before := len(sess.Draft.Sections)
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:remove:0"); err != nil {
		t.Fatal(err)
	}
	if len(sess.Draft.Sections) != before {
		t.Fatal("requesting deletion must require confirmation")
	}
	if err := b.handleAdminCallback(ctx, 100, user{ID: 100}, "adm:removeconfirm:0"); err != nil {
		t.Fatal(err)
	}
	if len(sess.Draft.Sections) != before-1 {
		t.Fatal("confirmed delete did not remove section")
	}
	if err := b.publishAdminDraft(ctx, 100, 100, sess); err != nil {
		t.Fatal(err)
	}
	changed, ok := b.store.Find("020")
	if !ok || len(changed.Sections) != before-1 {
		t.Fatal("public card did not reflect deletion")
	}
	if err := b.showAdminHistory(ctx, 100); err != nil {
		t.Fatal(err)
	}
	_, kb := lastAdminMessage(t, tr)
	if !strings.Contains(kb, "adm:histview:0") {
		t.Fatalf("no clickable history: %s", kb)
	}
	if err := b.showAdminHistoryEntry(ctx, 100, 0); err != nil {
		t.Fatal(err)
	}
	msg, _ := lastAdminMessage(t, tr)
	if !strings.Contains(msg, "Удалён:") {
		t.Fatalf("version diff missing deletion: %s", msg)
	}
	if err := b.confirmAdminHistoryRestore(ctx, 100, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.applyAdminHistoryRestore(ctx, 100, 100, 0); err != nil {
		t.Fatal(err)
	}
	restored, _ := b.store.Find("020")
	if len(restored.Sections) != before {
		t.Fatalf("section not restored: %+v", restored.Sections)
	}
	history, _ := b.adminHistory()
	if len(history) != 2 || history[1].Operation != "restore" {
		t.Fatalf("restoration was not recorded: %+v", history)
	}
}
func TestAdminRevisionRestoreRejectsConflictingEdit(t *testing.T) {
	b, tr := makeAdminTestBot(t)
	ctx := context.Background()
	if err := b.startAdminEditor(ctx, 100, 100, "020"); err != nil {
		t.Fatal(err)
	}
	s := b.adminSession(100)
	s.Draft.Title = "First"
	if err := b.publishAdminDraft(ctx, 100, 100, s); err != nil {
		t.Fatal(err)
	}
	if err := b.confirmAdminHistoryRestore(ctx, 100, 0); err != nil {
		t.Fatal(err)
	}
	loc, _ := b.store.Find("020")
	revision := catalog.Revision(loc)
	revision.Title = "Concurrent"
	if err := b.store.AdminEdit("020", catalog.RevisionHash(revision), revision); err != nil { // wrong old hash? revision modified here; reload correct
		revisionBefore := revision
		revisionBefore.Title = "First"
		if err := b.store.AdminEdit("020", catalog.RevisionHash(revisionBefore), revision); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.applyAdminHistoryRestore(ctx, 100, 100, 0); err != nil {
		t.Fatal(err)
	}
	loc, _ = b.store.Find("020")
	if loc.Title != "Concurrent" {
		t.Fatalf("stale restore overwrote concurrent change: %+v", loc)
	}
	msg, _ := lastAdminMessage(t, tr)
	if !strings.Contains(msg, "изменилась") {
		t.Fatalf("missing conflict warning: %s", msg)
	}
}
func TestInvalidAdminDraftInputLeavesOldContent(t *testing.T) {
	b, _ := makeAdminTestBot(t)
	ctx := context.Background()
	if err := b.startAdminEditor(ctx, 100, 100, "020"); err != nil {
		t.Fatal(err)
	}
	session := b.adminSession(100)
	old := session.Draft.Title
	session.Field = "title"
	if handled, err := b.handleAdminText(ctx, 100, user{ID: 100}, ""); !handled || err != nil {
		t.Fatalf("invalid input was not handled: %v %v", handled, err)
	}
	if session.Draft.Title != old {
		t.Fatal("invalid edit damaged the draft")
	}
}
func TestAutomaticBackupsRetainFourteenAndKeepData(t *testing.T) {
	b, _ := makeAdminTestBot(t)
	ctx := context.Background()
	begin := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 16; i++ {
		if err := b.runDailyBackup(ctx, begin.AddDate(0, 0, i)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := b.autoBackupList()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 14 || files[0] != "auto-2026-11-03.zip" {
		t.Fatalf("rotation broken: %+v", files)
	}
	raw, err := os.ReadFile(filepath.Join(b.autoBackupDirectory(), files[len(files)-1]))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw[:2]), "PK") {
		t.Fatal("invalid ZIP header")
	}
	mode, err := os.Stat(filepath.Join(b.autoBackupDirectory(), files[len(files)-1]))
	if err != nil {
		t.Fatal(err)
	}
	if mode.Mode().Perm() != 0600 {
		t.Fatalf("backup permissions too open: %v", mode.Mode())
	}
}
