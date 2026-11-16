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
	if keyboard != nil {
		payload["reply_markup"] = keyboard
	}
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
func (b *Bot) welcome(ctx context.Context, chatID int64) error {
	msg := "<b>Карта локаций</b> 🚇\n\nВыбери станцию по названию или отправь геопозицию, чтобы увидеть ближайшие объекты проекта. Я пришлю изображение из каталога, описание и оценку (если заполнены).\n\nНажми <b>📍 Рядом со мной</b> и разреши Telegram отправить местоположение один раз. Также можно отправить точку на карте вручную.\n\nПоиск: просто напиши название станции."
	return b.Send(ctx, chatID, msg, mainKeyboard())
}
func (b *Bot) showPage(ctx context.Context, chatID int64, page int) error {
	loc := b.store.List()
	const limit = 8
	maxPage := 0
	if len(loc) > 0 {
		maxPage = (len(loc) - 1) / limit
	}
	if page < 0 {
		page = 0
	}
	if page > maxPage {
		page = maxPage
	}
	start := page * limit
	end := start + limit
	if end > len(loc) {
		end = len(loc)
	}
	rows := make([][]Button, 0, limit+1)
	for _, l := range loc[start:end] {
		rows = append(rows, []Button{callbackButton(l.Title, "loc:"+l.ID)})
	}
	nav := []Button{}
	if page > 0 {
		nav = append(nav, callbackButton("◀️ Назад", fmt.Sprintf("page:%d", page-1)))
	}
	if page < maxPage {
		nav = append(nav, callbackButton("Вперёд ▶️", fmt.Sprintf("page:%d", page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	return b.Send(ctx, chatID, fmt.Sprintf("<b>Станции и локации</b>: %d объектов · страница %d/%d\nНажми на название, чтобы открыть карточку.", len(loc), page+1, maxPage+1), markup(rows...))
}
func (b *Bot) showLocation(ctx context.Context, chatID int64, id string) error {
	l, found := b.store.Find(id)
	if !found {
		return b.Send(ctx, chatID, "Станция не найдена. Открой /list.", nil)
	}
	lines := []string{"<b>" + html.EscapeString(l.Title) + "</b>"}
	if l.Category != "" {
		lines = append(lines, html.EscapeString(l.Category))
	}
	if l.Rating != "" {
		lines = append(lines, "⭐ Оценка: "+html.EscapeString(l.Rating))
	}
	if l.Description != "" {
		lines = append(lines, "\n"+html.EscapeString(l.Description))
	}
	caption := strings.Join(lines, "\n")
	// Telegram photo captions are limited to 1024 characters after entity parsing.
	// Escaped HTML is cut only before formatting, so there are no dangling tags.
	extra := ""
	if len([]rune(caption)) > 900 {
		short := []rune(l.Description)
		keep := 300
		if len(short) > keep {
			extra = html.EscapeString(l.Description)
			l.Description = string(short[:keep]) + "…"
		}
		lines = lines[:0]
		lines = append(lines, "<b>"+html.EscapeString(l.Title)+"</b>")
		if l.Category != "" {
			lines = append(lines, html.EscapeString(l.Category))
		}
		if l.Rating != "" {
			lines = append(lines, "⭐ Оценка: "+html.EscapeString(l.Rating))
		}
		if l.Description != "" {
			lines = append(lines, "\n"+html.EscapeString(l.Description))
		}
		caption = strings.Join(lines, "\n")
	}
	rows := [][]Button{}
	if l.Latitude != nil && l.Longitude != nil {
		point := fmt.Sprintf("https://yandex.ru/maps/?pt=%f,%f&z=16&l=map", *l.Longitude, *l.Latitude)
		rows = append(rows, []Button{urlButton("📍 Открыть точку", point)})
	}
	rows = append(rows, []Button{callbackButton("📋 Все станции", "page:0"), urlButton("🗺 Общая карта", b.mapURL)})
	kb := markup(rows...)
	if l.ImageURL != "" {
		if err := b.sendPhoto(ctx, chatID, l, caption, kb); err == nil {
			if extra != "" {
				return b.Send(ctx, chatID, "<b>Полное описание</b>\n"+extra, nil)
			}
			return nil
		} else {
			log.Printf("Photo %s: %v", l.ID, err)
		}
	}
	return b.Send(ctx, chatID, caption+"\n\n<i>Изображение появится после успешной синхронизации с Яндекс Диском.</i>", kb)
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
		return b.Send(ctx, chatID, "Пока не могу определить ближайшую станцию: в каталоге ещё нет проверенных координат. Администратору нужно заполнить <code>config/overrides.json</code>. Поиск по названию уже работает.", mainKeyboard())
	}
	rows := [][]Button{}
	for i, n := range near {
		rows = append(rows, []Button{callbackButton(fmt.Sprintf("%d. %s · %.1f км", i+1, n.Location.Title, n.DistanceKM), "loc:"+n.Location.ID)})
	}
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
		case strings.HasPrefix(u.Callback.Data, "loc:"):
			return b.showLocation(ctx, chatID, strings.TrimPrefix(u.Callback.Data, "loc:"))
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
		return b.Send(ctx, chatID, "Напиши название станции или часть названия. Например, <b>Троицк</b>.", mainKeyboard())
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
		return b.showLocation(ctx, chatID, found[0].ID)
	}
	rows := [][]Button{}
	for i, l := range found {
		if i >= 15 {
			break
		}
		rows = append(rows, []Button{callbackButton(l.Title, "loc:"+l.ID)})
	}
	return b.Send(ctx, chatID, fmt.Sprintf("Найдено: %d. Выбери станцию:", len(found)), markup(rows...))
}
func (b *Bot) showMap(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "<b>Карта проекта</b> 🗺", markup([]Button{urlButton("Открыть Яндекс Карту", b.mapURL)}))
}
func (b *Bot) requestLocation(ctx context.Context, chatID int64) error {
	return b.Send(ctx, chatID, "Нажми кнопку <b>📍 Рядом со мной</b> на клавиатуре и разреши отправку текущей геопозиции. Также можешь прикрепить произвольную точку через меню Telegram. Сведения о твоём местоположении не записываются в базу.", mainKeyboard())
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
