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
	"mime/multipart"
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

func mainKeyboard() any {
	return map[string]any{"keyboard": []any{
		[]any{map[string]any{"text": "📍 Рядом со мной", "request_location": true}},
		[]any{map[string]any{"text": "📋 Список станций"}, map[string]any{"text": "🔎 Поиск"}},
		[]any{map[string]any{"text": "🗺 Общая карта"}},
	}, "resize_keyboard": true, "is_persistent": true}
}
func New(token, constructorID, dataDir string, store *catalog.Store, sy *syncer.Syncer, admins string) *Bot {
	ids := map[int64]bool{}
	for _, value := range strings.Split(admins, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && id > 0 {
			ids[id] = true
		}
	}
	mapURL := "https://yandex.ru/maps/?um=" + url.QueryEscape("constructor:"+constructorID) + "&source=constructorLink"
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
func (b *Bot) answerCallback(ctx context.Context, id string) {
	_, _ = b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id})
}
func (b *Bot) sendPhoto(ctx context.Context, chatID int64, loc catalog.Location, caption string, kb any) error {
	file, err := os.Open(filepath.Join(b.store.DataDir(), "images", loc.ID+loc.ImageExt))
	if err != nil {
		return err
	}
	defer file.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	_ = mw.WriteField("caption", caption)
	_ = mw.WriteField("parse_mode", "HTML")
	if kb != nil {
		raw, _ := json.Marshal(kb)
		_ = mw.WriteField("reply_markup", string(raw))
	}
	part, err := mw.CreateFormFile("photo", loc.ID+loc.ImageExt)
	if err != nil {
		return err
	}
	if _, err = io.Copy(part, io.LimitReader(file, 11<<20)); err != nil {
		return err
	}
	if err = mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/sendPhoto", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var r apiResult
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("sendPhoto: %s", r.Description)
	}
	return nil
}

// sendPhotoAlbum sends a native Telegram album with three unmodified copies of
// the existing PNG. Telegram albums cannot carry an inline keyboard; the card
// with text and navigation is sent as a separate message immediately afterward.
func (b *Bot) sendPhotoAlbum(ctx context.Context, chatID int64, loc catalog.Location) error {
	imagePath := filepath.Join(b.store.DataDir(), "images", loc.ID+loc.ImageExt)
	imageBytes, err := os.ReadFile(imagePath)
	if err != nil {
		return err
	}
	if len(imageBytes) == 0 || len(imageBytes) > 10<<20 {
		return fmt.Errorf("invalid photo size: %d bytes", len(imageBytes))
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	media := make([]map[string]string, 0, 3)
	for i := 0; i < 3; i++ {
		field := fmt.Sprintf("photo%d", i)
		media = append(media, map[string]string{"type": "photo", "media": "attach://" + field})
		part, partErr := mw.CreateFormFile(field, fmt.Sprintf("%s-%d%s", loc.ID, i+1, loc.ImageExt))
		if partErr != nil {
			return partErr
		}
		if _, partErr = part.Write(imageBytes); partErr != nil {
			return partErr
		}
	}
	rawMedia, err := json.Marshal(media)
	if err != nil {
		return err
	}
	if err = mw.WriteField("media", string(rawMedia)); err != nil {
		return err
	}
	if err = mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/sendMediaGroup", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var result apiResult
	if err = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&result); err != nil {
		return err
	}
	if !result.OK {
		return fmt.Errorf("sendMediaGroup: %s", result.Description)
	}
	return nil
}

func (b *Bot) welcome(ctx context.Context, chatID int64) error {
	msg := "<b>Карта локаций</b> 🚇\n\nВыбери станцию по названию или отправь геопозицию, чтобы увидеть ближайшие объекты проекта. Я пришлю три фотографии локации и партнёрскую карточку с четырьмя разделами и ссылкой на карту (когда координаты будут известны).\n\nНажми <b>📍 Рядом со мной</b> и разреши Telegram отправить местоположение один раз. Также можно отправить точку на карте вручную.\n\nПоиск: просто напиши название станции."
	return b.Send(ctx, chatID, msg, mainKeyboard())
}

// Two columns on every page. Sort by the real station name, not "Метро" or image ID.
const stationPageSize = 10
const photoCaptionBudget = 900 // conservative UTF-16 budget; Telegram limits photo captions to 1024 characters.

func stationLabel(title string) string {
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
// place a pin at the exact coordinate. No data means no individual link.
func stationMapLink(l catalog.Location, originalLayerURL string) string {
	if direct := strings.TrimSpace(l.MapURL); direct != "" && len(direct) < 1500 {
		u, err := url.Parse(direct)
		if err == nil && u.Scheme == "https" && u.User == nil {
			host := strings.ToLower(u.Hostname())
			if host == "yandex.ru" || strings.HasSuffix(host, ".yandex.ru") || host == "yandex.com" || strings.HasSuffix(host, ".yandex.com") {
				return direct
			}
		}
	}
	if l.Latitude == nil || l.Longitude == nil {
		return ""
	}
	lat, lon := *l.Latitude, *l.Longitude
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return ""
	}
	u, err := url.Parse(originalLayerURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "yandex.ru" && host != "yandex.com" {
		return ""
	}
	coordinates := fmt.Sprintf("%.7f,%.7f", lon, lat)
	q := u.Query()
	q.Set("ll", coordinates)
	q.Set("pt", coordinates)
	q.Set("z", "16")
	q.Set("l", "map")
	u.RawQuery = q.Encode()
	return u.String()
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

	mapLine := "Индивидуальная ссылка появится после привязки координат."
	if link := stationMapLink(l, layerURL); link != "" {
		mapLine = `<a href="` + html.EscapeString(link) + `">Открыть точку в Яндекс Картах</a>`
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
	if l.ImageURL != "" {
		// Album in Telegram, then one full text card below, with clickable links
		// and navigation. Each album item reuses the exact original PNG bytes.
		if err := b.sendPhotoAlbum(ctx, chatID, l); err != nil {
			log.Printf("Album %s: %v (trying single photo)", l.ID, err)
			if err := b.sendPhoto(ctx, chatID, l, "📸 Фотографии и ориентиры: "+html.EscapeString(l.Title), nil); err != nil {
				log.Printf("Photo fallback %s: %v", l.ID, err)
				card += "\n\n<i>Фото временно недоступно.</i>"
			}
		}
	} else {
		card += "\n\n<i>Фотография появится после синхронизации с Яндекс Диском.</i>"
	}
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
	case "🗺 Общая карта":
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
	return b.Send(ctx, chatID, "<b>Карта проекта</b> 🗺", markup([]Button{urlButton("Открыть Яндекс Карту", b.mapURL)}, []Button{callbackButton("⬅️ К списку", "page:0"), menuButton()}))
}
func (b *Bot) requestLocation(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "Нажми кнопку <b>📍 Рядом со мной</b> на клавиатуре и разреши отправку текущей геопозиции. Также можешь прикрепить произвольную точку через меню Telegram. Сведения о твоём местоположении не записываются в базу.", markup([]Button{callbackButton("📋 Список станций", "page:0"), menuButton()}))
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
