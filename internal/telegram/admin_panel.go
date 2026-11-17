package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"locationbot/internal/adminbackup"
	"locationbot/internal/catalog"
)

type adminSession struct {
	LocationID     string
	OriginalHash   string
	Draft          catalog.CardRevision
	Field          string
	SectionIndex   int
	Backup         adminbackup.Archive
	HasBackup      bool
	AwaitingBackup bool
}
type adminRevisionEvent struct {
	At         time.Time            `json:"at"`
	Actor      int64                `json:"actor"`
	LocationID string               `json:"location_id"`
	Before     catalog.CardRevision `json:"before"`
	After      catalog.CardRevision `json:"after"`
	Operation  string               `json:"operation"`
}

func (b *Bot) isAdmin(userID int64) bool { return userID > 0 && b.adminIDs[userID] }
func (b *Bot) adminSession(userID int64) *adminSession {
	b.adminMu.Lock()
	defer b.adminMu.Unlock()
	if b.adminStates == nil {
		b.adminStates = map[int64]*adminSession{}
	}
	if b.adminStates[userID] == nil {
		b.adminStates[userID] = &adminSession{}
	}
	return b.adminStates[userID]
}
func (b *Bot) adminHomeMarkup(mapURL string, userID int64) any {
	rows := startMenuMarkup(mapURL)
	if !b.isAdmin(userID) {
		return rows
	}
	// Append the button without mutating the shared rows.
	if obj, ok := any(rows).(map[string]any); ok {
		raw, _ := obj["inline_keyboard"].([][]Button)
		added := append(append([][]Button{}, raw...), []Button{callbackButton("⚙️ Админ-панель", "adm:menu")})
		return markup(added...)
	}
	return rows
}
func (b *Bot) showAdminMenu(ctx context.Context, chatID int64) error {
	count := 0
	if b.store != nil {
		count = len(b.store.List())
	}
	leads := b.adminLeadCount()
	msg := fmt.Sprintf("⚙️ <b>Панель управления Karty</b>\n\n📍 Локаций: <b>%d</b>\n📨 Заявок: <b>%d</b>\n\nВыберите раздел:", count, leads)
	return b.Send(ctx, chatID, msg, markup(
		[]Button{callbackButton("📊 Аналитика", "adm:stats:views:30"), callbackButton("📍 Редактор локаций", "adm:locations:0")},
		[]Button{callbackButton("📦 Резервные копии", "adm:backup")},
		[]Button{callbackButton("🕘 История изменений", "adm:history")},
		[]Button{menuButton()},
	))
}
func (b *Bot) adminLeadCount() int {
	// Kept in the runtime volume; avoid depending on the admin UI's state.
	raw, err := os.ReadFile(filepath.Join(b.store.DataDir(), "leads.json"))
	if err != nil {
		return 0
	}
	var state struct {
		Submitted map[string]json.RawMessage `json:"submitted"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return 0
	}
	return len(state.Submitted)
}
func (b *Bot) adminLeadDates() map[string][]time.Time {
	raw, err := os.ReadFile(filepath.Join(b.store.DataDir(), "leads.json"))
	if err != nil {
		return nil
	}
	var state struct {
		Submitted map[string]struct {
			LocationID string    `json:"location_id"`
			CreatedAt  time.Time `json:"created_at"`
		} `json:"submitted"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return nil
	}
	result := map[string][]time.Time{}
	for _, app := range state.Submitted {
		if app.LocationID != "" {
			result[app.LocationID] = append(result[app.LocationID], app.CreatedAt)
		}
	}
	return result
}
func (b *Bot) adminCurrentFavorites() map[string]int {
	result := map[string]int{}
	if b.favorites == nil {
		return result
	}
	b.favorites.mu.RLock()
	defer b.favorites.mu.RUnlock()
	for _, ids := range b.favorites.byUser {
		for _, id := range ids {
			result[id]++
		}
	}
	return result
}
func (b *Bot) recordLocationAnalytics(userID int64, id, kind string) {
	if b.metrics == nil || b.isAdmin(userID) {
		return
	}
	if _, ok := b.store.Find(id); !ok {
		return
	}
	if err := b.metrics.Record(userID, id, kind, time.Now().UTC()); err != nil {
		log.Printf("analytics event: %v", err)
	}
}
func (b *Bot) showAdminStats(ctx context.Context, chatID int64, metric string, days int) error {
	switch metric {
	case "views", "unique", "added", "saved", "leads":
	default:
		metric = "views"
	}
	if days != 7 && days != 30 && days != 0 {
		days = 30
	}
	ids := []string{}
	names := map[string]string{}
	for _, loc := range b.store.List() {
		ids = append(ids, loc.ID)
		names[loc.ID] = publicText(loc.Title)
	}
	if b.metrics == nil {
		return b.Send(ctx, chatID, "Аналитика временно недоступна.", nil)
	}
	scores := b.metrics.Rankings(time.Now().UTC(), days, metric, ids, b.adminCurrentFavorites(), b.adminLeadDates())
	labels := map[string]string{"views": "👁 Просмотры", "unique": "👤 Уникальные просмотры", "added": "⭐ Добавления в избранное", "saved": "⭐ Сейчас в избранном", "leads": "🎯 Заявки"}
	period := "Все время"
	if days > 0 {
		period = fmt.Sprintf("%d дней", days)
	}
	var text strings.Builder
	text.WriteString("📊 <b>ТОП-10 локаций</b>\n" + labels[metric] + " · " + period + "\n\n")
	for i, r := range scores {
		number := r.Views
		switch metric {
		case "unique":
			number = r.UniqueViews
		case "added":
			number = r.Additions
		case "saved":
			number = r.CurrentFavorites
		case "leads":
			number = r.Leads
		}
		text.WriteString(fmt.Sprintf("%d. <b>%s</b> — %d\n", i+1, html.EscapeString(names[r.LocationID]), number))
	}
	if metric == "views" || metric == "unique" {
		text.WriteString("\n<em>Визиты — открытия карточек в боте, не физические посещения. Старые просмотры не восстанавливаются.</em>")
	}
	if metric == "added" {
		text.WriteString("\n<em>История добавлений ведётся с момента включения аналитики.</em>")
	}
	return b.Send(ctx, chatID, text.String(), markup(
		[]Button{callbackButton("👁 Просмотры", "adm:stats:views:"+strconv.Itoa(days)), callbackButton("⭐ Добавления", "adm:stats:added:"+strconv.Itoa(days))},
		[]Button{callbackButton("👤 Уникальные", "adm:stats:unique:"+strconv.Itoa(days)), callbackButton("🎯 Заявки", "adm:stats:leads:"+strconv.Itoa(days))},
		[]Button{callbackButton("⭐ Сейчас сохранено", "adm:stats:saved:"+strconv.Itoa(days))},
		[]Button{callbackButton("7 дней", "adm:stats:"+metric+":7"), callbackButton("30 дней", "adm:stats:"+metric+":30"), callbackButton("Всё время", "adm:stats:"+metric+":0")},
		[]Button{callbackButton("⬅️ Админ-панель", "adm:menu")},
	))
}
func (b *Bot) showAdminLocations(ctx context.Context, chatID int64, page int) error {
	locations := sortedStations(b.store.List())
	pages := (len(locations) + 9) / 10
	if pages == 0 {
		pages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	from := page * 10
	until := from + 10
	if until > len(locations) {
		until = len(locations)
	}
	rows := [][]Button{}
	for i := from; i < until; i++ {
		rows = append(rows, []Button{callbackButton(buttonLabel(locations[i].Title), "adm:edit:"+locations[i].ID)})
	}
	nav := []Button{}
	if page > 0 {
		nav = append(nav, callbackButton("◀️", fmt.Sprintf("adm:locations:%d", page-1)))
	}
	if page+1 < pages {
		nav = append(nav, callbackButton("▶️", fmt.Sprintf("adm:locations:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []Button{callbackButton("🔎 Найти локацию", "adm:locsearch")})
	rows = append(rows, []Button{callbackButton("⬅️ Админ-панель", "adm:menu")})
	return b.Send(ctx, chatID, fmt.Sprintf("📍 <b>Редактор локаций</b>\nСтраница %d из %d. Выберите карточку для редактирования:", page+1, pages), markup(rows...))
}
func (b *Bot) startAdminEditor(ctx context.Context, chatID, userID int64, id string) error {
	loc, ok := b.store.Find(id)
	if !ok {
		return b.Send(ctx, chatID, "Локация не найдена.", nil)
	}
	session := b.adminSession(userID)
	session.LocationID = id
	session.Draft = catalog.Revision(loc)
	session.OriginalHash = catalog.RevisionHash(session.Draft)
	session.Field = ""
	return b.showAdminEditor(ctx, chatID, session)
}
func (b *Bot) showAdminEditor(ctx context.Context, chatID int64, s *adminSession) error {
	name := html.EscapeString(s.Draft.Title)
	rows := [][]Button{
		{callbackButton("✏️ Название", "adm:field:title"), callbackButton("📌 Статус", "adm:field:line_status")},
		{callbackButton("📍 Ориентир", "adm:field:landmarks"), callbackButton("🗺 Ссылка карты", "adm:field:map_url")},
		{callbackButton("🧩 Разделы карточки", "adm:sections")},
		{callbackButton("👁 Предпросмотр", "adm:preview"), callbackButton("✅ Опубликовать", "adm:publish")},
		{callbackButton("❌ Отменить", "adm:locations:0"), callbackButton("⬅️ Админ-панель", "adm:menu")},
	}
	return b.Send(ctx, chatID, fmt.Sprintf("✏️ <b>Редактор карточки</b>\n\n<b>%s</b> · ID <code>%s</code>\nРазделов: %d\n\nИзменения не увидят пользователи до публикации.", name, html.EscapeString(s.LocationID), len(s.Draft.Sections)), markup(rows...))
}
func (b *Bot) showAdminSections(ctx context.Context, chatID int64, s *adminSession) error {
	rows := [][]Button{}
	for i, section := range s.Draft.Sections {
		mark := "✅"
		if !section.Enabled {
			mark = "🚫"
		}
		rows = append(rows, []Button{callbackButton(fmt.Sprintf("%s %d. %s", mark, i+1, limitRunes(section.Title, 45)), fmt.Sprintf("adm:section:%d", i))})
	}
	rows = append(rows, []Button{callbackButton("➕ Добавить раздел", "adm:add")})
	rows = append(rows, []Button{callbackButton("⬅️ К карточке", "adm:editor")})
	return b.Send(ctx, chatID, "🧩 <b>Разделы карточки</b>\n\nНажмите нужный раздел для изменения текста, названия, порядка или видимости.", markup(rows...))
}
func (b *Bot) showAdminSection(ctx context.Context, chatID int64, s *adminSession, i int) error {
	if i < 0 || i >= len(s.Draft.Sections) {
		return b.showAdminSections(ctx, chatID, s)
	}
	section := s.Draft.Sections[i]
	label := "Показывается"
	if !section.Enabled {
		label = "Скрыт"
	}
	rows := [][]Button{
		{callbackButton("✏️ Заголовок", fmt.Sprintf("adm:sectiontitle:%d", i)), callbackButton("📝 Описание", fmt.Sprintf("adm:sectionbody:%d", i))},
		{callbackButton("◀️ Выше", fmt.Sprintf("adm:up:%d", i)), callbackButton("Ниже ▶️", fmt.Sprintf("adm:down:%d", i))},
		{callbackButton("👁 Скрыть / показать", fmt.Sprintf("adm:toggle:%d", i)), callbackButton("🗑 Удалить", fmt.Sprintf("adm:remove:%d", i))},
		{callbackButton("⬅️ К разделам", "adm:sections")},
	}
	return b.Send(ctx, chatID, fmt.Sprintf("🧩 <b>%s</b>\nСостояние: %s\n\n%s", html.EscapeString(section.Title), label, html.EscapeString(limitRunes(section.Body, 650))), markup(rows...))
}
func (b *Bot) renderAdminPreview(ctx context.Context, chatID int64, s *adminSession) error {
	loc, found := b.store.Find(s.LocationID)
	if !found {
		return errors.New("location missing")
	}
	loc.Title = s.Draft.Title
	loc.LineStatus = s.Draft.LineStatus
	loc.Landmarks = s.Draft.Landmarks
	loc.MapURL = s.Draft.MapURL
	loc.Sections = append([]catalog.CardSection{}, s.Draft.Sections...)
	return b.Send(ctx, chatID, "👁 <b>Предпросмотр карточки</b>\n\n"+cardPreviewHTML(loc, b.mapURL), markup([]Button{callbackButton("⬅️ Редактировать", "adm:editor"), callbackButton("✅ Опубликовать", "adm:publish")}))
}
func cardPreviewHTML(l catalog.Location, mapURL string) string {
	// Rich preview uses simple HTML markup that standard Telegram clients can
	// render, while production Rich Messages keep their native details.
	var buf strings.Builder
	buf.WriteString("📍 <b>" + html.EscapeString(l.Title) + "</b>\n")
	buf.WriteString("<i>" + html.EscapeString(l.LineStatus) + "</i>\nОриентир: " + html.EscapeString(l.Landmarks) + "\n")
	for _, s := range l.Sections {
		if s.Enabled {
			buf.WriteString("\n<b>" + html.EscapeString(s.Title) + "</b>\n" + html.EscapeString(s.Body) + "\n")
		}
	}
	return buf.String()
}
func (b *Bot) adminHistoryPath() string {
	return filepath.Join(b.store.DataDir(), "admin_history.json")
}
func (b *Bot) adminHistory() ([]adminRevisionEvent, error) {
	data, err := os.ReadFile(b.adminHistoryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []adminRevisionEvent
	if err = json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}
func (b *Bot) recordAdminHistory(entry adminRevisionEvent) error {
	entries, err := b.adminHistory()
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicAdminFile(b.adminHistoryPath(), data)
}
func atomicAdminFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".admin-history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (b *Bot) publishAdminDraft(ctx context.Context, chatID, userID int64, s *adminSession) error {
	loc, ok := b.store.Find(s.LocationID)
	if !ok {
		return errors.New("missing location")
	}
	before := catalog.Revision(loc)
	if catalog.RevisionHash(before) == catalog.RevisionHash(s.Draft) {
		return b.Send(ctx, chatID, "Нет новых изменений для публикации.", markup([]Button{callbackButton("⬅️ К редактору", "adm:editor")}))
	}
	if err := b.store.AdminEdit(s.LocationID, s.OriginalHash, s.Draft); err != nil {
		return b.Send(ctx, chatID, "Не удалось опубликовать: "+html.EscapeString(err.Error()), markup([]Button{callbackButton("⬅️ К редактору", "adm:editor")}))
	}
	entry := adminRevisionEvent{At: time.Now().UTC(), Actor: userID, LocationID: s.LocationID, Before: before, After: s.Draft, Operation: "publish"}
	if err := b.recordAdminHistory(entry); err != nil {
		log.Printf("Admin history write failed: %v", err)
		return b.Send(ctx, chatID, "Карточка опубликована, но журнал изменений не сохранился. Рекомендуется сделать резервную копию.", markup([]Button{callbackButton("⬅️ Админка", "adm:menu")}))
	}
	s.OriginalHash = catalog.RevisionHash(s.Draft)
	return b.Send(ctx, chatID, "✅ <b>Карточка опубликована!</b>\n\nВсе новые открытия карточки будут показывать обновлённые данные.", markup([]Button{callbackButton("✏️ Продолжить редактирование", "adm:editor")}, []Button{callbackButton("⬅️ Админ-панель", "adm:menu")}))
}
func (b *Bot) showAdminHistory(ctx context.Context, chatID int64) error {
	records, err := b.adminHistory()
	if err != nil {
		return err
	}
	var msg strings.Builder
	msg.WriteString("🕘 <b>История изменений карточек</b>\n")
	rows := [][]Button{}
	for i := len(records) - 1; i >= 0 && i >= len(records)-10; i-- {
		item := records[i]
		msg.WriteString(fmt.Sprintf("\n%s · %s · %s · администратор %d", item.At.Format("02.01 15:04"), html.EscapeString(item.LocationID), html.EscapeString(item.Operation), item.Actor))
		if i == len(records)-1 {
			rows = append(rows, []Button{callbackButton("↩️ Откатить последнюю правку", "adm:undo")})
		}
	}
	if len(records) == 0 {
		msg.WriteString("\n\nИзменений пока нет.")
	}
	rows = append(rows, []Button{callbackButton("⬅️ Админ-панель", "adm:menu")})
	return b.Send(ctx, chatID, msg.String(), markup(rows...))
}
func (b *Bot) undoLastAdminChange(ctx context.Context, chatID, userID int64) error {
	records, err := b.adminHistory()
	if err != nil {
		return err
	}
	if len(records) == 0 {
		return b.showAdminHistory(ctx, chatID)
	}
	last := records[len(records)-1]
	current, ok := b.store.Find(last.LocationID)
	if !ok {
		return errors.New("missing location")
	}
	if err := b.store.AdminEdit(last.LocationID, catalog.RevisionHash(last.After), last.Before); err != nil {
		return err
	}
	if err := b.recordAdminHistory(adminRevisionEvent{At: time.Now().UTC(), Actor: userID, LocationID: last.LocationID, Before: catalog.Revision(current), After: last.Before, Operation: "undo"}); err != nil {
		return err
	}
	return b.Send(ctx, chatID, "✅ Предыдущая редакция карточки восстановлена.", markup([]Button{callbackButton("🕘 История", "adm:history")}))
}
func (b *Bot) showBackupMenu(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "📦 <b>Резервные копии Karty</b>\n\nПолный ZIP содержит заявки, текущие редакции карточек, избранное, историю изменений и аналитику. Отправка и восстановление доступны только администраторам в личном чате.", markup(
		[]Button{callbackButton("📥 Скачать полный ZIP", "adm:export")},
		[]Button{callbackButton("📤 Загрузить бэкап", "adm:import")},
		[]Button{callbackButton("⬅️ Админ-панель", "adm:menu")},
	))
}
func (b *Bot) sendAdminBackup(ctx context.Context, chatID int64) error {
	data, err := adminbackup.Build(b.store.DataDir(), b.store.List())
	if err != nil {
		return err
	}
	filename := "karty-backup-" + time.Now().UTC().Format("20060102-150405") + ".zip"
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err = writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	file, err := writer.CreateFormFile("document", filename)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+b.token+"/sendDocument", &buffer)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result apiResult
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("Telegram sendDocument: %s", result.Description)
	}
	return nil
}
func (b *Bot) receiveAdminBackup(ctx context.Context, chatID, userID int64, fileID string, size int64) error {
	if fileID == "" || size > adminbackup.MaxZIP || size < 0 {
		return b.Send(ctx, chatID, "Архив слишком большой или не распознан. Максимум 15 МБ.", nil)
	}
	raw, err := b.call(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return err
	}
	var response struct {
		FilePath string `json:"file_path"`
		FileSize int64  `json:"file_size"`
	}
	if err = json.Unmarshal(raw, &response); err != nil {
		return err
	}
	path := response.FilePath
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || path == "" {
		return errors.New("invalid Telegram file path")
	}
	if response.FileSize > adminbackup.MaxZIP {
		return errors.New("backup too large")
	}
	safePath := strings.Split(path, "/")
	for _, part := range safePath {
		if part == "" || part == "." || part == ".." {
			return errors.New("invalid Telegram file path")
		}
	}
	downloadURL := "https://api.telegram.org/file/bot" + url.PathEscape(b.token) + "/" + strings.Join(safePath, "/")
	request, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := b.http.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Telegram file download status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, adminbackup.MaxZIP+1))
	if err != nil {
		return err
	}
	if len(data) > adminbackup.MaxZIP {
		return errors.New("backup download exceeds limit")
	}
	archive, err := adminbackup.Examine(data)
	if err != nil {
		return b.Send(ctx, chatID, "❌ Архив не прошёл проверку: "+html.EscapeString(err.Error()), nil)
	}
	session := b.adminSession(userID)
	session.Backup = archive
	session.HasBackup = true
	session.AwaitingBackup = false
	inspection := archive.Inspect
	return b.Send(ctx, chatID, fmt.Sprintf("📦 <b>Проверка резервной копии</b>\n\nДата: %s UTC\nЛокаций: %d\nЗаявок: %d\nСохранений: %d\n\nВыберите режим. Перед восстановлением бот создаст защитный бэкап.", inspection.CreatedAt.Format("02.01.2006 15:04"), inspection.Locations, inspection.Leads, inspection.Favorites), markup(
		[]Button{callbackButton("📍 Только карточки", "adm:restore:cards")},
		[]Button{callbackButton("⚠️ Полное восстановление", "adm:restore:full")},
		[]Button{callbackButton("❌ Отмена", "adm:backup")},
	))
}
func (b *Bot) restoreAdminBackup(ctx context.Context, chatID, userID int64, mode string) error {
	session := b.adminSession(userID)
	if !session.HasBackup {
		return b.showBackupMenu(ctx, chatID)
	}
	// Serialize restoration with background lead delivery and favorites writes.
	if mode == "full" {
		if b.leads != nil {
			b.leads.mu.Lock()
			defer b.leads.mu.Unlock()
		}
		if b.favorites != nil {
			b.favorites.mu.Lock()
			defer b.favorites.mu.Unlock()
		}
	}
	path, err := adminbackup.Restore(b.store.DataDir(), session.Backup, mode, b.store.List())
	if err != nil {
		return b.Send(ctx, chatID, "❌ Восстановление не удалось: "+html.EscapeString(err.Error()), nil)
	}
	if err = b.store.ReloadAdminCards(); err != nil {
		return fmt.Errorf("imported catalog reload failed (protective snapshot %s): %w", path, err)
	}
	if mode == "full" {
		fav, err := openFavoriteStore(filepath.Join(b.store.DataDir(), "favorites.json"))
		if err != nil {
			return err
		}
		if b.favorites != nil {
			b.favorites.byUser = fav.byUser
		} else {
			b.favorites = fav
		}
		if b.metrics != nil {
			if err = b.metrics.Reload(); err != nil {
				return err
			}
		}
		if err := b.reloadLeadsAfterRestore(); err != nil {
			return err
		}
	}
	session.HasBackup = false
	return b.Send(ctx, chatID, "✅ <b>Данные восстановлены.</b>\n\nЗащитная копия предыдущего состояния сохранена на сервере. Формат: ZIP.", markup([]Button{callbackButton("⬅️ Админ-панель", "adm:menu")}))
}

// The current version of Karty exposes a leadStore; this is separated to
// keep the backup workflow testable without users' private information.
func (b *Bot) reloadLeadsAfterRestore() error {
	newStore, err := openLeadStore(filepath.Join(b.store.DataDir(), "leads.json"))
	if err != nil {
		return err
	}
	if b.leads != nil {
		b.leads.state = newStore.state
	} else {
		b.leads = newStore
	}
	return nil
}

func (b *Bot) handleAdminCallback(ctx context.Context, chatID int64, from user, data string) error {
	if !b.isAdmin(from.ID) || chatID != from.ID {
		return nil
	}
	session := b.adminSession(from.ID)
	parts := strings.Split(data, ":")
	switch {
	case data == "adm:menu":
		session.Field = ""
		return b.showAdminMenu(ctx, chatID)
	case data == "adm:backup":
		session.Field = ""
		session.AwaitingBackup = false
		session.HasBackup = false
		return b.showBackupMenu(ctx, chatID)
	case data == "adm:export":
		if err := b.sendAdminBackup(ctx, chatID); err != nil {
			return err
		}
		return b.showBackupMenu(ctx, chatID)
	case data == "adm:import":
		session.AwaitingBackup = true
		return b.Send(ctx, chatID, "📤 <b>Загрузить резервную копию</b>\n\nОтправьте ZIP-файл сюда, в личный чат. После проверки архива Вы сможете выбрать тип восстановления.", markup([]Button{callbackButton("⬅️ Отмена", "adm:backup")}))
	case data == "adm:restore:full" || data == "adm:restore:cards":
		if !session.HasBackup {
			return b.showBackupMenu(ctx, chatID)
		}
		mode := strings.TrimPrefix(data, "adm:restore:")
		return b.Send(ctx, chatID, "⚠️ <b>Подтверждение восстановления</b>\n\nВы действительно хотите восстановить данные из архива? Перед операцией будет создан защитный ZIP.", markup(
			[]Button{callbackButton("✅ Да, восстановить", "adm:apply:"+mode)},
			[]Button{callbackButton("❌ Отмена", "adm:backup")},
		))
	case strings.HasPrefix(data, "adm:apply:"):
		return b.restoreAdminBackup(ctx, chatID, from.ID, strings.TrimPrefix(data, "adm:apply:"))
	case data == "adm:history":
		return b.showAdminHistory(ctx, chatID)
	case data == "adm:undo":
		return b.undoLastAdminChange(ctx, chatID, from.ID)
	case strings.HasPrefix(data, "adm:stats:") && len(parts) == 4:
		period, err := strconv.Atoi(parts[3])
		if err != nil {
			period = 30
		}
		return b.showAdminStats(ctx, chatID, parts[2], period)
	case strings.HasPrefix(data, "adm:locations:"):
		session.Field = ""
		page, err := strconv.Atoi(strings.TrimPrefix(data, "adm:locations:"))
		if err != nil {
			page = 0
		}
		return b.showAdminLocations(ctx, chatID, page)
	case strings.HasPrefix(data, "adm:edit:"):
		return b.startAdminEditor(ctx, chatID, from.ID, strings.TrimPrefix(data, "adm:edit:"))
	case data == "adm:locsearch":
		session.Field = "search_location"
		return b.Send(ctx, chatID, "🔎 <b>Поиск локации</b>\n\nВведите часть названия локации, которую хотите изменить.", markup([]Button{callbackButton("⬅️ К каталогу", "adm:locations:0")}))
	case data == "adm:editor":
		session.Field = ""
		return b.showAdminEditor(ctx, chatID, session)
	case data == "adm:sections":
		session.Field = ""
		return b.showAdminSections(ctx, chatID, session)
	case data == "adm:preview":
		session.Field = ""
		return b.renderAdminPreview(ctx, chatID, session)
	case data == "adm:publish":
		session.Field = ""
		return b.publishAdminDraft(ctx, chatID, from.ID, session)
	case strings.HasPrefix(data, "adm:field:"):
		field := strings.TrimPrefix(data, "adm:field:")
		switch field {
		case "title", "line_status", "landmarks", "map_url":
		default:
			return nil
		}
		session.Field = field
		return b.Send(ctx, chatID, "✏️ Отправьте новое значение поля <b>"+html.EscapeString(field)+"</b> обычным сообщением. Изменение останется черновиком до публикации.", markup([]Button{callbackButton("❌ Отмена", "adm:editor")}))
	case strings.HasPrefix(data, "adm:section:"):
		n, err := strconv.Atoi(strings.TrimPrefix(data, "adm:section:"))
		if err != nil {
			return nil
		}
		return b.showAdminSection(ctx, chatID, session, n)
	case strings.HasPrefix(data, "adm:sectiontitle:") || strings.HasPrefix(data, "adm:sectionbody:"):
		n := 0
		field := ""
		if strings.HasPrefix(data, "adm:sectiontitle:") {
			n, _ = strconv.Atoi(strings.TrimPrefix(data, "adm:sectiontitle:"))
			field = "section_title"
		} else {
			n, _ = strconv.Atoi(strings.TrimPrefix(data, "adm:sectionbody:"))
			field = "section_body"
		}
		if n < 0 || n >= len(session.Draft.Sections) {
			return b.showAdminSections(ctx, chatID, session)
		}
		session.Field = field
		session.SectionIndex = n
		return b.Send(ctx, chatID, "✏️ Напишите новое значение текстом. Нажмите «Опубликовать» в редакторе, когда будете готовы.", markup([]Button{callbackButton("❌ Отмена", "adm:sections")}))
	case data == "adm:add":
		if len(session.Draft.Sections) >= 15 {
			return b.Send(ctx, chatID, "Достигнут предел 15 разделов.", nil)
		}
		index := len(session.Draft.Sections)
		session.Draft.Sections = append(session.Draft.Sections, catalog.CardSection{ID: fmt.Sprintf("extra-%d", time.Now().UTC().UnixNano()), Title: "Новый раздел", Body: "Добавьте описание", Enabled: true})
		return b.showAdminSection(ctx, chatID, session, index)
	case strings.HasPrefix(data, "adm:toggle:") || strings.HasPrefix(data, "adm:remove:") || strings.HasPrefix(data, "adm:up:") || strings.HasPrefix(data, "adm:down:"):
		op := parts[1]
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 0 || n >= len(session.Draft.Sections) {
			return nil
		}
		switch op {
		case "toggle", "remove":
			session.Draft.Sections[n].Enabled = !session.Draft.Sections[n].Enabled
		case "up":
			if n > 0 {
				session.Draft.Sections[n], session.Draft.Sections[n-1] = session.Draft.Sections[n-1], session.Draft.Sections[n]
			}
			n--
		case "down":
			if n+1 < len(session.Draft.Sections) {
				session.Draft.Sections[n], session.Draft.Sections[n+1] = session.Draft.Sections[n+1], session.Draft.Sections[n]
			}
			n++
		}
		return b.showAdminSections(ctx, chatID, session)
	}
	return nil
}
func (b *Bot) handleAdminText(ctx context.Context, chatID int64, from user, input string) (bool, error) {
	if !b.isAdmin(from.ID) || chatID != from.ID {
		return false, nil
	}
	session := b.adminSession(from.ID)
	if session.Field == "search_location" {
		if strings.HasPrefix(input, "/") {
			return false, nil
		}
		matches := catalog.Search(b.store.List(), input)
		rows := [][]Button{}
		for i, l := range matches {
			if i >= 12 {
				break
			}
			rows = append(rows, []Button{callbackButton(buttonLabel(l.Title), "adm:edit:"+l.ID)})
		}
		rows = append(rows, []Button{callbackButton("⬅️ К каталогу", "adm:locations:0")})
		if len(matches) == 0 {
			return true, b.Send(ctx, chatID, "Совпадений не найдено. Введите другой запрос.", markup(rows...))
		}
		session.Field = ""
		return true, b.Send(ctx, chatID, "🔎 <b>Найденные локации</b>\n\nВыберите карточку:", markup(rows...))
	}
	if session.Field == "" || session.LocationID == "" {
		return false, nil
	}
	if strings.HasPrefix(input, "/") {
		return false, nil
	}
	input = strings.TrimSpace(input)
	if len([]rune(input)) > 3500 {
		return true, b.Send(ctx, chatID, "Слишком длинный текст (максимум 3500 символов).", nil)
	}
	switch session.Field {
	case "title":
		session.Draft.Title = input
	case "line_status":
		session.Draft.LineStatus = input
	case "landmarks":
		session.Draft.Landmarks = input
	case "map_url":
		session.Draft.MapURL = input
	case "section_title":
		session.Draft.Sections[session.SectionIndex].Title = input
	case "section_body":
		session.Draft.Sections[session.SectionIndex].Body = input
	default:
		return false, nil
	}
	if err := session.Draft.Validate(); err != nil {
		return true, b.Send(ctx, chatID, "Некорректное значение: "+html.EscapeString(err.Error()), nil)
	}
	session.Field = ""
	return true, b.showAdminEditor(ctx, chatID, session)
}
