package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"locationbot/internal/catalog"
	"locationbot/internal/syncer"
)

type Bot struct {
	token, mapURL, offsetFile string
	store                     *catalog.Store
	syncer                    *syncer.Syncer
	adminIDs                  map[int64]bool
	http                      *http.Client
}
type user struct {
	ID int64 `json:"id"`
}
type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type geoPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}
type message struct {
	MessageID int       `json:"message_id"`
	Text      string    `json:"text"`
	Chat      chat      `json:"chat"`
	From      user      `json:"from"`
	Location  *geoPoint `json:"location"`
}
type callbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	From    user     `json:"from"`
	Message *message `json:"message"`
}
type update struct {
	UpdateID int64          `json:"update_id"`
	Message  *message       `json:"message"`
	Callback *callbackQuery `json:"callback_query"`
}
type apiResult struct {
	OK          bool            `json:"ok"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}
type Button map[string]any

func urlButton(title, dest string) Button      { return Button{"text": title, "url": dest} }
func callbackButton(title, data string) Button { return Button{"text": title, "callback_data": data} }
func markup(rows ...[]Button) map[string]any   { return map[string]any{"inline_keyboard": rows} }
func menuButton() Button                       { return callbackButton("🏠 Главное меню", "menu") }
func homeMarkup() any                          { return markup([]Button{menuButton()}) }

// mainKeyboard is shown only after "📍 Рядом со мной" is selected from the inline menu.
// Telegram supports request_location only in a reply keyboard.
func mainKeyboard() any {
	return map[string]any{"keyboard": []any{
		[]any{map[string]any{"text": "📍 Отправить геопозицию", "request_location": true}},
	}, "resize_keyboard": true, "one_time_keyboard": true}
}

func startMenuMarkup(mapURL string) any {
	return markup(
		[]Button{callbackButton("📍 Рядом со мной", "near")},
		[]Button{callbackButton("📋 Список станций", "list"), callbackButton("🔎 Поиск", "search")},
		[]Button{urlButton("🗺 Открыть карту", mapURL)},
	)
}
const defaultProjectMapURL = "https://yandex.ru/maps/?um=constructor%3A4e091ad95afb487039d07dc0667d6d596da173fe30cff48c7f2e234b7150d7f0&source=constructorLink"

func New(token, constructorID, dataDir string, store *catalog.Store, sy *syncer.Syncer, admins string) *Bot {
	ids := map[int64]bool{}
	for _, value := range strings.Split(admins, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && id > 0 {
			ids[id] = true
		}
	}
	mapURL := defaultProjectMapURL
	if constructorID = strings.TrimSpace(constructorID); constructorID != "" {
		mapURL = "https://yandex.ru/maps/?um=" + url.QueryEscape("constructor:"+constructorID) + "&source=constructorLink"
	}
	return &Bot{token: token, mapURL: mapURL, store: store, syncer: sy, adminIDs: ids, http: &http.Client{Timeout: 50 * time.Second}, offsetFile: filepath.Join(dataDir, "telegram-offset.txt")}
}
func (b *Bot) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := b.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var result apiResult
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("Telegram %s: %s", method, result.Description)
	}
	return result.Result, nil
}
func (b *Bot) Send(ctx context.Context, chatID int64, text string, keyboard any) error {
	payload := map[string]any{"chat_id": chatID, "text": text, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}
	if keyboard == nil {
		keyboard = homeMarkup()
	}
	payload["reply_markup"] = keyboard
	_, err := b.call(ctx, "sendMessage", payload)
	return err
}

// sendWelcome removes a reply keyboard left by an older version, then attaches
// the inline start menu to that same welcome message.
func (b *Bot) sendWelcome(ctx context.Context, chatID int64, text string, keyboard any) error {
	payload := map[string]any{
		"chat_id": chatID,
		"text": text,
		"parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
		"reply_markup": map[string]any{"remove_keyboard": true},
	}
	raw, err := b.call(ctx, "sendMessage", payload)
	if err != nil {
		return err
	}
	var sent struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil {
		return err
	}
	if sent.MessageID == 0 {
		return fmt.Errorf("Telegram sendMessage returned no message_id")
	}
	_, attachErr := b.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID,
		"message_id": sent.MessageID,
		"reply_markup": keyboard,
	})
	if attachErr == nil {
		return nil
	}

	// If the client/API rejects adding inline markup after the keyboard-removal
	// message, send a fresh welcome with its inline keyboard already attached.
	log.Printf("Attach start menu to message %d: %v; retrying with a new message", sent.MessageID, attachErr)
	payload["reply_markup"] = keyboard
	if _, sendErr := b.call(ctx, "sendMessage", payload); sendErr != nil {
		return fmt.Errorf("attach start menu: %v; fallback send: %w", attachErr, sendErr)
	}
	if _, deleteErr := b.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": sent.MessageID}); deleteErr != nil {
		log.Printf("Remove temporary welcome message %d: %v", sent.MessageID, deleteErr)
	}
	return nil
}
func (b *Bot) answerCallback(ctx context.Context, id string) {
	_, _ = b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id})
}
func (b *Bot) welcome(ctx context.Context, chatID int64) error {
	msg := "<b>Карта локаций</b> 🚇\n\nВыбери станцию по названию или отправь геопозицию — я покажу ближайшие объекты проекта, если для них заданы координаты. При выборе станции пришлю партнёрскую карточку с четырьмя разделами и ссылкой на карту.\n\nНажми <b>📍 Рядом со мной</b> под этим сообщением, чтобы отправить геопозицию. Также можно отправить точку вручную.\n\nПоиск: просто напиши название станции."
	return b.sendWelcome(ctx, chatID, msg, startMenuMarkup(b.mapURL))
}

// Two columns on every page. Sort by the real station name, not "Метро" or image ID.
const stationPageSize = 10
remove photo caption budgetfunc stationLabel(title string) string {
	title = strings.TrimSpace(title)
	if strings.HasPrefix(strings.ToLower(title), "метро ") {
		return strings.TrimSpace(string([]rune(title)[len([]rune("Метро ")):]))
	}
	return title
}

func stationSortKey(title string) string {
	return strings.ToLower(strings.ReplaceAll(stationLabel(title), "ё", "е"))
}

func sortedStations(items []catalog.Location) []catalog.Location {
	out := append([]catalog.Location(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := stationSortKey(out[i].Title), stationSortKey(out[j].Title)
		if a == b {
			return out[i].ID < out[j].ID
		}
		return a < b
	})
	return out
}

func limitRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if max <= 0 {
		return ""
	}
	if len(r) <= max {
		return string(r)
	}
	if max == 1 {
		return "…"
	}
	return strings.TrimSpace(string(r[:max-1])) + "…"
}

func buttonLabel(title string) string {
	// Keep labels compact on mobile; full name remains in the station card.
	return limitRunes(stationLabel(title), 22)
}

func stationPageRows(items []catalog.Location, page int) ([][]Button, int) {
	stations := sortedStations(items)
	lastPage := 0
	if len(stations) > 0 {
		lastPage = (len(stations) - 1) / stationPageSize
	}
	if page < 0 {
		page = 0
	}
	if page > lastPage {
		page = lastPage
	}
	start := page * stationPageSize
	end := min(start+stationPageSize, len(stations))
	rows := make([][]Button, 0, 8)
	for i := start; i < end; i += 2 {
		row := []Button{callbackButton(buttonLabel(stations[i].Title), fmt.Sprintf("loc:%s:%d", stations[i].ID, page))}
		if i+1 < end {
			row = append(row, callbackButton(buttonLabel(stations[i+1].Title), fmt.Sprintf("loc:%s:%d", stations[i+1].ID, page)))
		}
		rows = append(rows, row)
	}
	nav := []Button{}
	if page > 0 {
		nav = append(nav, callbackButton("◀️ Назад", fmt.Sprintf("page:%d", page-1)))
	}
	if page < lastPage {
		nav = append(nav, callbackButton("Далее ▶️", fmt.Sprintf("page:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, []Button{menuButton()})
	return rows, lastPage
}

func (b *Bot) showPage(ctx context.Context, chatID int64, page int) error {
	items := b.store.List()
	rows, maxPage := stationPageRows(items, page)
	if page < 0 {
		page = 0
	}
	if page > maxPage {
		page = maxPage
	}
	return b.Send(ctx, chatID, fmt.Sprintf("<b>🚇 Станции от А до Я</b> · %d объектов\nСтраница %d/%d. Выбери станцию:", len(items), page+1, maxPage+1), markup(rows...))
}

// Telegram text messages are limited to 4096 characters; keep the complete
// station template together with one image album wherever possible.
const cardTextBudget = 3700

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func limitCaptionUnits(s string, maxUnits int) string {
	s = strings.TrimSpace(s)
	if maxUnits <= 0 {
		return ""
	}
	if utf16Len(s) <= maxUnits {
		return s
	}
	if maxUnits == 1 {
		return "…"
	}
	units := 0
	r := []rune{}
	for _, ch := range s {
		step := 1
		if ch > 0xFFFF {
			step = 2
		}
		if units+step > maxUnits-1 {
			break
		}
		r = append(r, ch)
		units += step
	}
	return strings.TrimSpace(string(r)) + "…"
}

// stationMapLink never silently redirects a station to the general map.
// An explicit Yandex link copied from the source project takes priority.
// Otherwise, verified coordinates center the original Constructor layer and
// place a pin at the exact coordinate. Without coordinates, use the project map.
func validatedYandexMapURL(raw string) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) >= 1500 {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return nil, false
	}
	host := strings.ToLower(u.Hostname())
	if host != "yandex.ru" && !strings.HasSuffix(host, ".yandex.ru") && host != "yandex.com" && !strings.HasSuffix(host, ".yandex.com") {
		return nil, false
	}
	return u, true
}

func stationMapLink(l catalog.Location, originalLayerURL string) string {
	if direct := strings.TrimSpace(l.MapURL); direct != "" {
		if _, ok := validatedYandexMapURL(direct); ok {
			return direct
		}
	}
	layer, ok := validatedYandexMapURL(originalLayerURL)
	if !ok {
		return ""
	}
	if l.Latitude == nil || l.Longitude == nil {
		return layer.String()
	}
	lat, lon := *l.Latitude, *l.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return layer.String()
	}
	coordinates := fmt.Sprintf("%.7f,%.7f", lon, lat)
	q := layer.Query()
	q.Set("ll", coordinates)
	q.Set("pt", coordinates)
	q.Set("z", "16")
	q.Set("l", "map")
	layer.RawQuery = q.Encode()
	return layer.String()
}
// cardField renders a safe, consistent placeholder for the 39 not-yet-filled
// partner cards. It never asserts facts about an unverified station.
func cardField(s string) string {
	if strings.TrimSpace(s) == "" {
		return "Информация пока не добавлена."
	}
	return limitCaptionUnits(strings.TrimSpace(s), 620)
}

func cardForLocation(l catalog.Location, layerURL string) string {
	title := limitCaptionUnits(l.Title, 150)
	lineStatus := strings.TrimSpace(l.LineStatus)
	if lineStatus == "" {
		lineStatus = "Линия и статус пока не указаны."
	}
	landmark := strings.TrimSpace(l.Landmarks)
	if landmark == "" {
		landmark = "Пока не указан."
	}
	traffic := l.Traffic
	// Keep older manually entered generic description visible until it has
	// been moved to a structured section. Do not replace the 4-part template.
	if strings.TrimSpace(traffic) == "" && strings.TrimSpace(l.Description) != "" {
		traffic = l.Description
	}

	mapLine := "Ссылка на карту пока не настроена."
	if link := stationMapLink(l, layerURL); link != "" {
		mapLine = `<a href="` + html.EscapeString(link) + `">Открыть карту</a>`
	}
	lines := []string{
		"🚇 <b>" + html.EscapeString(title) + "</b>",
		"<b>КОПИРКА · КАРТА ЛОКАЦИЙ ДЛЯ ПАРТНЁРОВ</b>",
		"",
		"🚇 " + html.EscapeString(limitCaptionUnits(lineStatus, 200)),
		"📍 <b>Ориентир:</b> " + html.EscapeString(limitCaptionUnits(landmark, 310)),
		"",
		"🚶 <b>1. ТРАФИК И ЛОКАЦИЯ</b>",
		html.EscapeString(cardField(traffic)),
		"",
		"👥 <b>2. КТО ПЛАТИТ</b>",
		html.EscapeString(cardField(l.Payers)),
		"",
		"📈 <b>3. ОКНО ВОЗМОЖНОСТЕЙ</b>",
		html.EscapeString(cardField(l.Opportunity)),
		"",
		"🎯 <b>4. ИТОГ: ПОЧЕМУ ИМЕННО ЗДЕСЬ</b>",
		html.EscapeString(cardField(l.Conclusion)),
	}
	if rating := strings.TrimSpace(l.Rating); rating != "" {
		lines = append(lines, "", "⭐ <b>Оценка:</b> "+html.EscapeString(limitCaptionUnits(rating, 100)))
	}
	lines = append(lines, "", "🗺 <b>Локация на карте:</b> "+mapLine)
	return strings.Join(lines, "\n")
}

func locationBackRows(backPage int) [][]Button {
	if backPage < 0 {
		backPage = 0
	}
	return [][]Button{
		{callbackButton("⬅️ К списку", fmt.Sprintf("page:%d", backPage)), callbackButton("🔎 Поиск станции", "search")},
		{menuButton()},
	}
}

func (b *Bot) requestStationSearch(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "🔎 <b>Поиск станции</b>\n\nНапиши название станции или его часть. Например, <b>Троицк</b>.", markup(
		[]Button{callbackButton("⬅️ К списку", "page:0")},
		[]Button{menuButton()},
	))
}

func (b *Bot) showLocation(ctx context.Context, chatID int64, id string, backPage int) error {
	l, found := b.store.Find(id)
	if !found {
		return b.Send(ctx, chatID, "Станция не найдена. Открой список станций.", markup(locationBackRows(backPage)...))
	}
	card := cardForLocation(l, b.mapURL)
	return b.Send(ctx, chatID, card, markup(locationBackRows(backPage)...))
}

type Nearby struct {
	Location   catalog.Location
	DistanceKM float64
}

func Nearest(loc []catalog.Location, lat, lon float64, limit int) []Nearby {
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 || math.IsNaN(lat) || math.IsNaN(lon) || limit <= 0 {
		return nil
	}
	res := []Nearby{}
	for _, l := range loc {
		if l.Latitude == nil || l.Longitude == nil {
			continue
		}
		a, b := *l.Latitude, *l.Longitude
		if a < -90 || a > 90 || b < -180 || b > 180 || math.IsNaN(a) || math.IsNaN(b) {
			continue
		}
		res = append(res, Nearby{Location: l, DistanceKM: distanceKM(lat, lon, a, b)})
	}
	sort.Slice(res, func(i, j int) bool {
		if res[i].DistanceKM == res[j].DistanceKM {
			return res[i].Location.ID < res[j].Location.ID
		}
		return res[i].DistanceKM < res[j].DistanceKM
	})
	if len(res) > limit {
		res = res[:limit]
	}
	return res
}
func distanceKM(aLat, aLon, bLat, bLon float64) float64 {
	const radius = 6371.0088
	rad := math.Pi / 180
	latDelta := (bLat - aLat) * rad
	lonDelta := (bLon - aLon) * rad
	h := math.Pow(math.Sin(latDelta/2), 2) + math.Cos(aLat*rad)*math.Cos(bLat*rad)*math.Pow(math.Sin(lonDelta/2), 2)
	return 2 * radius * math.Asin(math.Sqrt(math.Min(1, h)))
}
func (b *Bot) showNear(ctx context.Context, chatID int64, p *geoPoint) error {
	if p == nil {
		return nil
	}
	near := Nearest(b.store.List(), p.Latitude, p.Longitude, 5)
	if len(near) == 0 {
		return b.Send(ctx, chatID, "Пока не могу определить ближайшую станцию: в каталоге ещё нет проверенных координат. Для поиска по геопозиции необходим экспорт точек из исходной карты. Поиск по названию уже работает.", markup([]Button{callbackButton("📋 Список станций", "page:0"), menuButton()}))
	}
	rows := [][]Button{}
	for i, n := range near {
		rows = append(rows, []Button{callbackButton(fmt.Sprintf("%d. %s · %.1f км", i+1, stationLabel(n.Location.Title), n.DistanceKM), "loc:"+n.Location.ID)})
	}
	rows = append(rows, []Button{callbackButton("⬅️ К списку", "page:0"), menuButton()})
	return b.Send(ctx, chatID, "<b>Ближайшие локации</b> 📍\nРасстояния рассчитаны <b>по прямой</b>, а не по маршруту. Нажми на станцию, чтобы посмотреть фото, описание и оценку.\n\nГеопозицию бот не сохраняет.", markup(rows...))
}
func (b *Bot) handle(ctx context.Context, u update) error {
	if u.Callback != nil {
		b.answerCallback(ctx, u.Callback.ID)
		if u.Callback.Message == nil {
			return nil
		}
		chatID := u.Callback.Message.Chat.ID
		if u.Callback.Message.Chat.Type != "private" {
			return nil
		}
		switch {
		case u.Callback.Data == "menu":
			return b.welcome(ctx, chatID)
		case u.Callback.Data == "near":
			return b.requestLocation(ctx, chatID)
		case u.Callback.Data == "list":
			return b.showPage(ctx, chatID, 0)
		case u.Callback.Data == "search":
			return b.requestStationSearch(ctx, chatID)
		case strings.HasPrefix(u.Callback.Data, "loc:"):
			parts := strings.Split(strings.TrimPrefix(u.Callback.Data, "loc:"), ":")
			if len(parts) == 0 {
				return nil
			}
			page := 0
			if len(parts) > 1 {
				page, _ = strconv.Atoi(parts[1])
			}
			return b.showLocation(ctx, chatID, parts[0], page)
		case strings.HasPrefix(u.Callback.Data, "page:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(u.Callback.Data, "page:"))
			return b.showPage(ctx, chatID, n)
		}
		return nil
	}
	m := u.Message
	if m == nil || m.Chat.Type != "private" {
		return nil
	}
	if m.Location != nil {
		if err := b.Send(ctx, m.Chat.ID, "Геопозицию получил.", map[string]any{"remove_keyboard": true}); err != nil {
			return err
		}
		return b.showNear(ctx, m.Chat.ID, m.Location)
	}
	chatID := m.Chat.ID
	input := strings.TrimSpace(m.Text)
	if input == "" {
		return nil
	}
	switch input {
	case "📋 Список станций":
		return b.showPage(ctx, chatID, 0)
	case "🔎 Поиск":
		return b.requestStationSearch(ctx, chatID)
	case "🗺 Открыть карту", "🗺 Общая карта":
		return b.showMap(ctx, chatID)
	case "📍 Рядом со мной":
		return b.requestLocation(ctx, chatID)
	}
	cmd := strings.SplitN(strings.ToLower(input), " ", 2)[0]
	if at := strings.IndexByte(cmd, '@'); at >= 0 {
		cmd = cmd[:at]
	}
	switch cmd {
	case "/start", "/help":
		return b.welcome(ctx, chatID)
	case "/list":
		return b.showPage(ctx, chatID, 0)
	case "/near":
		return b.requestLocation(ctx, chatID)
	case "/map":
		return b.showMap(ctx, chatID)
	case "/id":
		return b.Send(ctx, chatID, fmt.Sprintf("Твой Telegram ID: <code>%d</code>", m.From.ID), nil)
	case "/reload":
		if !b.adminIDs[m.From.ID] {
			return b.Send(ctx, chatID, "Доступно только администратору.", nil)
		}
		if err := b.store.Reload(); err != nil {
			return b.Send(ctx, chatID, "Ошибка конфигурации: "+html.EscapeString(err.Error()), nil)
		}
		return b.Send(ctx, chatID, "Данные из overrides.json обновлены.", nil)
	case "/sync":
		if !b.adminIDs[m.From.ID] {
			return b.Send(ctx, chatID, "Доступно только администратору. ID: /id", nil)
		}
		_ = b.Send(ctx, chatID, "Обновляю фотографии из Яндекс Диска…", nil)
		c, cancel := context.WithTimeout(ctx, 7*time.Minute)
		defer cancel()
		r, err := b.syncer.Run(c)
		if err != nil {
			return b.Send(ctx, chatID, "Синхронизация не удалась: "+html.EscapeString(err.Error()), nil)
		}
		reply := fmt.Sprintf("Файлов: %d · скачано: %d · без изменений: %d · ошибок: %d", r.Total, r.Downloaded, r.Skipped, len(r.Failures))
		if len(r.Failures) > 0 {
			reply += "\n" + html.EscapeString(strings.Join(r.Failures[:min(3, len(r.Failures))], "\n"))
		}
		return b.Send(ctx, chatID, reply, nil)
	case "/status":
		if !b.adminIDs[m.From.ID] {
			return b.Send(ctx, chatID, "Доступно только администратору.", nil)
		}
		items := b.store.List()
		pics, coords, ratings := 0, 0, 0
		for _, l := range items {
			if l.ImageURL != "" {
				pics++
			}
			if l.Latitude != nil && l.Longitude != nil {
				coords++
			}
			if l.Rating != "" {
				ratings++
			}
		}
		return b.Send(ctx, chatID, fmt.Sprintf("Локаций: %d\nФотографий: %d\nС координатами: %d\nС оценками: %d", len(items), pics, coords, ratings), nil)
	}
	if strings.HasPrefix(cmd, "/") {
		return b.Send(ctx, chatID, "Неизвестная команда. /help", nil)
	}
	found := catalog.Search(b.store.List(), input)
	if len(found) == 0 {
		return b.Send(ctx, chatID, "По запросу <b>"+html.EscapeString(input)+"</b> ничего не найдено. Попробуй часть названия.", nil)
	}
	if len(found) == 1 {
		return b.showLocation(ctx, chatID, found[0].ID, 0)
	}
	found = sortedStations(found)
	rows := [][]Button{}
	for i := 0; i < len(found) && i < 14; i += 2 {
		row := []Button{callbackButton(buttonLabel(found[i].Title), "loc:"+found[i].ID)}
		if i+1 < len(found) && i+1 < 14 {
			row = append(row, callbackButton(buttonLabel(found[i+1].Title), "loc:"+found[i+1].ID))
		}
		rows = append(rows, row)
	}
	rows = append(rows, []Button{callbackButton("⬅️ К списку", "page:0"), menuButton()})
	return b.Send(ctx, chatID, fmt.Sprintf("Найдено: %d. Выбери станцию:", len(found)), markup(rows...))
}
func (b *Bot) showMap(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "<b>Карта проекта</b> 🗺", markup([]Button{urlButton("Открыть карту", b.mapURL)}, []Button{callbackButton("⬅️ К списку", "page:0"), menuButton()}))
}
func (b *Bot) requestLocation(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "Нажми кнопку <b>📍 Отправить геопозицию</b> ниже и разреши Telegram отправить текущую точку. Также можно выбрать точку вручную через меню вложений. Геопозиция не сохраняется в базе.", mainKeyboard())
}
func (b *Bot) Run(ctx context.Context) error {
	if _, err := b.call(ctx, "getMe", map[string]any{}); err != nil {
		return fmt.Errorf("Bot token verification: %w", err)
	}
	_, _ = b.call(ctx, "setMyCommands", map[string]any{"commands": []map[string]string{
		{"command": "start", "description": "Главное меню"}, {"command": "list", "description": "Список станций"}, {"command": "near", "description": "Ближайшие станции"}, {"command": "map", "description": "Исходная карта"}, {"command": "id", "description": "Мой Telegram ID"},
	}})
	// Explicitly remove any Mini App menu configured by an older deployment.
	_, _ = b.call(ctx, "setChatMenuButton", map[string]any{"menu_button": map[string]string{"type": "commands"}})
	var offset int64
	if raw, err := os.ReadFile(b.offsetFile); err == nil {
		offset, _ = strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	}
	log.Println("Telegram long polling started (no Mini App, no open ports)")
	for ctx.Err() == nil {
		raw, err := b.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 30, "limit": 30, "allowed_updates": []string{"message", "callback_query"}})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("getUpdates: %v", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(4 * time.Second):
				continue
			}
		}
		var updates []update
		if err = json.Unmarshal(raw, &updates); err != nil {
			log.Printf("Invalid getUpdates: %v", err)
			continue
		}
		for _, u := range updates {
			if err = b.handle(ctx, u); err != nil {
				log.Printf("Update %d: %v", u.UpdateID, err)
			}
			offset = u.UpdateID + 1
			if err = os.WriteFile(b.offsetFile, []byte(strconv.FormatInt(offset, 10)), 0600); err != nil {
				log.Printf("Update offset save: %v", err)
			}
		}
	}
	return nil
}
