package telegram

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"locationbot/internal/catalog"
)

func point(v float64) *float64 { return &v }

func TestNearestSortedAndWithoutCoordinates(t *testing.T) {
	items := []catalog.Location{
		{ID: "far", Title: "Дальняя", Latitude: point(55.8), Longitude: point(37.9)},
		{ID: "missing", Title: "Не определена"},
		{ID: "close", Title: "Близкая", Latitude: point(55.751), Longitude: point(37.617)},
		{ID: "zero", Title: "В той же точке", Latitude: point(55.75), Longitude: point(37.617)},
	}
	got := Nearest(items, 55.75, 37.617, 2)
	if len(got) != 2 || got[0].Location.ID != "zero" || got[1].Location.ID != "close" {
		t.Fatalf("unexpected nearest: %+v", got)
	}
	if got[0].DistanceKM != 0 || got[1].DistanceKM <= 0 || got[1].DistanceKM > 1 {
		t.Fatalf("bad distance: %+v", got)
	}
	if len(Nearest(items, 91, 37, 3)) != 0 {
		t.Fatal("invalid latitude accepted")
	}
	if len(Nearest(items, math.NaN(), 37, 3)) != 0 {
		t.Fatal("NaN accepted")
	}
	if len(Nearest([]catalog.Location{{ID: "001"}}, 55, 37, 3)) != 0 {
		t.Fatal("station without coordinates returned")
	}
}
func TestRequestLocationButton(t *testing.T) {
	raw, err := json.Marshal(mainKeyboard())
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Keyboard [][]struct {
			Text            string `json:"text"`
			RequestLocation bool   `json:"request_location"`
		} `json:"keyboard"`
	}
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Keyboard) == 0 || len(payload.Keyboard[0]) == 0 || !payload.Keyboard[0][0].RequestLocation {
		t.Fatal("no Telegram native location button")
	}
}
func TestHaversine(t *testing.T) {
	if d := distanceKM(55.75, 37.62, 55.75, 37.62); d != 0 {
		t.Fatalf("same point returned %f km", d)
	}
	if d := distanceKM(0, 0, 0, 1); d < 110 || d > 112 {
		t.Fatalf("one degree longitude at equator: %f", d)
	}
}

func TestStationSortRussianAndTwoColumnPages(t *testing.T) {
	items := []catalog.Location{
		{ID: "017", Title: "Метро Троицк"},
		{ID: "002", Title: "Метро Кленовый бульвар"},
		{ID: "038", Title: "Фрязино"},
		{ID: "035", Title: "МГСУ"},
		{ID: "010", Title: "Метро Народное Ополчение"},
		{ID: "036", Title: "Ивантеевка"},
		{ID: "011", Title: "Метро Липовая Роща"},
		{ID: "039", Title: "Королёв 2"},
		{ID: "040", Title: "Королёв 3"},
		{ID: "005", Title: "Метро Кавказский бульвар"},
		{ID: "008", Title: "Метро Лебедянская"},
	}
	sorted := sortedStations(items)
	if sorted[0].ID != "036" || sorted[1].ID != "005" || sorted[len(sorted)-1].ID != "038" {
		t.Fatalf("incorrect alphabet sorting: %+v", sorted)
	}
	if stationLabel("Метро Кленовый бульвар") != "Кленовый бульвар" {
		t.Fatal("metro prefix should be removed in labels")
	}
	rows, maxPage := stationPageRows(items, 0)
	if maxPage != 1 || len(rows) != 7 {
		t.Fatalf("unexpected pagination: %d, %d", maxPage, len(rows))
	}
	for i := 0; i < 5; i++ {
		if len(rows[i]) != 2 {
			t.Fatalf("row %d not two columns", i)
		}
	}
	if rows[len(rows)-1][0]["callback_data"] != "menu" {
		t.Fatal("missing main menu")
	}
	second, _ := stationPageRows(items, 1)
	if len(second[0]) != 1 || len(second[1]) != 1 || second[1][0]["callback_data"] != "page:0" {
		t.Fatalf("last page or Back incorrect: %+v", second)
	}
}
func TestCardTextAndHTMLSafety(t *testing.T) {
	loc := catalog.Location{ID: "001", Title: "Метро <Остров> & Мечты", Description: strings.Repeat("Станция <тест> & название. ", 150), Rating: "8/10", Landmarks: "Выход рядом с <ориентиром>"}
	text := cardForLocation(loc, "https://yandex.ru/maps/?um=constructor%3Amap")
	if !strings.Contains(text, "&lt;Остров&gt;") || !strings.Contains(text, "&lt;тест&gt;") || !strings.Contains(text, "&lt;ориентиром&gt;") {
		t.Fatal("Unescaped HTML:", text)
	}
	shown := html.UnescapeString(strings.ReplaceAll(strings.ReplaceAll(text, "<b>", ""), "</b>", ""))
	if utf16Len(shown) > 4096 {
		t.Fatalf("too long for Telegram: %d", utf16Len(shown))
	}
	if !strings.Contains(text, "Ориентир") || !strings.Contains(text, "8/10") || !strings.Contains(text, "🚶 <b>1. ТРАФИК И ЛОКАЦИЯ</b>") || !strings.Contains(text, "👥 <b>2. КТО ПЛАТИТ</b>") || !strings.Contains(text, "📈 <b>3. ОКНО ВОЗМОЖНОСТЕЙ</b>") || !strings.Contains(text, "🎯 <b>4. ИТОГ: ПОЧЕМУ ИМЕННО ЗДЕСЬ</b>") {
		t.Fatal("missing sections")
	}
	if !strings.Contains(text, "<a href=\"https://yandex.ru/maps/?um=constructor%3Amap\">Открыть карту</a>") {
		t.Fatal("project map fallback link was not displayed")
	}
	placeholder := cardForLocation(catalog.Location{Title: "Тест"}, "https://yandex.ru/maps/?um=constructor%3Amap")
	if !strings.Contains(placeholder, "Информация пока не добавлена.") || !strings.Contains(placeholder, "Пока не указан.") || !strings.Contains(placeholder, "4. ИТОГ: ПОЧЕМУ ИМЕННО ЗДЕСЬ") || strings.Contains(placeholder, "кафе «Чебурашка»") || strings.Contains(placeholder, "Оценка:") {
		t.Fatal("missing placeholders")
	}
}
func TestStationMapLinksArePerPoint(t *testing.T) {
	base := "https://yandex.ru/maps/?um=constructor%3Amap-from-owner&source=constructorLink"
	a := catalog.Location{ID: "001", Latitude: point(55.751), Longitude: point(37.620)}
	b := catalog.Location{ID: "002", Latitude: point(55.620), Longitude: point(37.750)}
	first, second := stationMapLink(a, base), stationMapLink(b, base)
	if first == "" || second == "" || first == second {
		t.Fatalf("stations must have unique links: %q / %q", first, second)
	}
	u, err := url.Parse(first)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("um") != "constructor:map-from-owner" || u.Query().Get("pt") != "37.6200000,55.7510000" || u.Query().Get("ll") != "37.6200000,55.7510000" {
		t.Fatalf("source layer or coordinates were lost: %s", first)
	}
	if got := stationMapLink(catalog.Location{ID: "003"}, base); got != base {
		t.Fatalf("project map fallback missing without station coordinates: %q", got)
	}
	direct := "https://yandex.ru/maps/?whatshere%5Bpoint%5D=37.7%2C55.7"
	if got := stationMapLink(catalog.Location{MapURL: direct}, base); got != direct {
		t.Fatalf("exact source link not preferred: %q", got)
	}
	if got := stationMapLink(catalog.Location{MapURL: "https://evil.test/steal"}, base); got != base {
		t.Fatalf("unsafe station URL should fall back to the validated project map: %q", got)
	}
	card := cardForLocation(a, base)
	if !strings.Contains(card, "Открыть карту") || !strings.Contains(card, "<a href=") {
		t.Fatalf("per-station hyperlink missing: %s", card)
	}
}
func TestCardNavigation(t *testing.T) {
	rows := locationBackRows(3)
	if len(rows) != 2 || len(rows[0]) != 2 || len(rows[1]) != 1 {
		t.Fatalf("expected two main buttons and menu on bottom: %+v", rows)
	}
	if rows[0][0]["callback_data"] != "page:3" || rows[0][1]["callback_data"] != "search" || rows[1][0]["callback_data"] != "menu" {
		t.Fatalf("incorrect callbacks: %+v", rows)
	}
}

type captureTransport struct {
	requests []string
	card     map[string]any
	payloads []map[string]any
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req.URL.Path)
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	switch req.URL.Path {
	case "/botTEST/sendMessage":
		var payload map[string]any
		if err = json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		c.card = payload
		c.payloads = append(c.payloads, payload)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"message_id":100}}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
}
func TestLocationCardOmitsPhotoAndLinksToProjectMap(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.json")
	if err := os.WriteFile(seed, []byte("[{\"id\":\"017\",\"title\":\"Метро Троицк\",\"filename\":\"017.png\",\"image_ext\":\".png\",\"description\":\"Тестовое описание\",\"rating\":\"8/10\"}]"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "017.png"), []byte("image present but photos are disabled"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.New(dir, seed, filepath.Join(dir, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &captureTransport{}
	projectMap := "https://yandex.ru/maps/?um=constructor%3Atest&source=constructorLink"
	b := &Bot{token: "TEST", mapURL: projectMap, store: store, http: &http.Client{Transport: tr}}
	if err := b.showLocation(context.Background(), 123, 456, "017", 2); err != nil {
		t.Fatal(err)
	}
	if len(tr.requests) != 1 || tr.requests[0] != "/botTEST/sendMessage" {
		t.Fatalf("station card should be sent as one text message without photos: %v", tr.requests)
	}
	text, ok := tr.card["text"].(string)
	if !ok || !strings.Contains(text, "Тестовое описание") || !strings.Contains(text, "<a href=\"https://yandex.ru/maps/?um=constructor%3Atest&amp;source=constructorLink\">Открыть карту</a>") {
		t.Fatalf("station text or project map link missing: %+v", tr.card)
	}
	kb, ok := tr.card["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("missing keyboard: %+v", tr.card)
	}
	keyboardBytes, _ := json.Marshal(kb)
	if !strings.Contains(string(keyboardBytes), "page:2") || !strings.Contains(string(keyboardBytes), "search") || !strings.Contains(string(keyboardBytes), "menu") {
		t.Fatalf("missing nav buttons: %s", keyboardBytes)
	}
	if !strings.Contains(string(keyboardBytes), "fav:toggle:017:2") || !strings.Contains(string(keyboardBytes), "Добавить в избранное") {
		t.Fatalf("favorite button missing from station card: %s", keyboardBytes)
	}
}

func TestStartCommandSendsPersistentMenuWithAllButtons(t *testing.T) {
	tr := &captureTransport{}
	b := &Bot{token: "TEST", mapURL: defaultProjectMapURL, http: &http.Client{Transport: tr}}
	message := &message{
		Text: "/start",
		Chat: chat{ID: 123, Type: "private"},
		From: user{ID: 456, FirstName: "Иван", LastName: "Иванов"},
	}
	if err := b.handle(context.Background(), update{Message: message}); err != nil {
		t.Fatal(err)
	}
	if len(tr.payloads) != 1 {
		t.Fatalf("start must send one welcome without temporary keyboard reset; got %d messages", len(tr.payloads))
	}
	welcome := tr.payloads[0]
	text, ok := welcome["text"].(string)
	if !ok || !strings.Contains(text, "Привет, Иван Иванов!") {
		t.Fatalf("personalized greeting missing: %v", welcome["text"])
	}
	markup, ok := welcome["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("welcome message has no reply keyboard: %+v", welcome)
	}
	if markup["is_persistent"] != true || markup["resize_keyboard"] != true || markup["inline_keyboard"] != nil || markup["remove_keyboard"] != nil {
		t.Fatalf("expected persistent reply keyboard, got %+v", markup)
	}
	rows, ok := markup["keyboard"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("expected 3 rows of main menu buttons, got %+v", markup["keyboard"])
	}
	keyboard, _ := json.Marshal(markup)
	for _, value := range []string{"📍 Рядом со мной", "📋 Список станций", "🔎 Поиск", "⭐ Избранное", "🗺 Открыть карту"} {
		if !strings.Contains(string(keyboard), value) {
			t.Errorf("start menu button %q missing from reply keyboard: %s", value, keyboard)
		}
	}
}

func TestMainMenuMapButtonOpensLink(t *testing.T) {
	tr := &captureTransport{}
	b := &Bot{token: "TEST", mapURL: defaultProjectMapURL, http: &http.Client{Transport: tr}}
	msg := &message{Text: "🗺 Открыть карту", Chat: chat{ID: 123, Type: "private"}, From: user{ID: 456}}
	if err := b.handle(context.Background(), update{Message: msg}); err != nil {
		t.Fatal(err)
	}
	if len(tr.payloads) != 1 {
		t.Fatalf("expected one response to map button, got %d", len(tr.payloads))
	}
	markup, _ := tr.payloads[0]["reply_markup"].(map[string]any)
	rows, _ := markup["inline_keyboard"].([]any)
	if len(rows) == 0 {
		t.Fatalf("map button has no URL row: %+v", markup)
	}
	row, _ := rows[0].([]any)
	if len(row) == 0 {
		t.Fatalf("map button has an empty URL row: %+v", markup)
	}
	button, _ := row[0].(map[string]any)
	if button["url"] != defaultProjectMapURL {
		t.Fatalf("map button URL incorrect: %+v", button)
	}
}

func TestLocationRequestKeepsMenuAndLocationAnswerRestoresIt(t *testing.T) {
	keyboard, _ := json.Marshal(mainKeyboard())
	for _, text := range []string{"request_location", "📋 Список станций", "🔎 Поиск", "⭐ Избранное", "🗺 Открыть карту"} {
		if !strings.Contains(string(keyboard), text) {
			t.Fatalf("location request lost %q: %s", text, keyboard)
		}
	}
	tr := &captureTransport{}
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.json")
	if err := os.WriteFile(seed, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.New(dir, seed, filepath.Join(dir, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{token: "TEST", store: store, http: &http.Client{Transport: tr}}
	msg := &message{Chat: chat{ID: 123, Type: "private"}, From: user{ID: 456}, Location: &geoPoint{Latitude: 55.75, Longitude: 37.61}}
	if err := b.handle(context.Background(), update{Message: msg}); err != nil {
		t.Fatal(err)
	}
	if len(tr.payloads) != 2 {
		t.Fatalf("expected keyboard restore and nearby results, got %d messages", len(tr.payloads))
	}
	markup, _ := tr.payloads[0]["reply_markup"].(map[string]any)
	if markup["is_persistent"] != true || markup["remove_keyboard"] != nil {
		t.Fatalf("location response must restore persistent main menu, got %+v", markup)
	}
}

func TestEmptyFavoritesProvidesStationShortcuts(t *testing.T) {
	dir := t.TempDir()
	favorites, err := openFavoriteStore(filepath.Join(dir, "favorites.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &captureTransport{}
	b := &Bot{token: "TEST", favorites: favorites, http: &http.Client{Transport: tr}}
	if err := b.showFavorites(context.Background(), 123, 456); err != nil {
		t.Fatal(err)
	}
	text, _ := tr.card["text"].(string)
	if !strings.Contains(text, "Пока здесь пусто — сохраним первую станцию?") || !strings.Contains(text, "Сохранённые станции появятся здесь") {
		t.Fatalf("empty-favorites copy missing: %s", text)
	}
	markup, _ := tr.card["reply_markup"].(map[string]any)
	keyboard, _ := json.Marshal(markup)
	for _, value := range []string{"list", "search", "menu"} {
		if !strings.Contains(string(keyboard), value) {
			t.Errorf("empty favorites shortcut %q missing: %s", value, keyboard)
		}
	}
}

func TestWelcomeTextFallsBackWithoutNameAndEscapesTelegramHTML(t *testing.T) {
	if got := welcomeText(user{}); !strings.HasPrefix(got, "Привет! 👋") {
		t.Fatalf("missing generic greeting fallback: %q", got)
	}
	if got := welcomeText(user{FirstName: "<Иван>"}); !strings.Contains(got, "Привет, &lt;Иван&gt;! 👋") {
		t.Fatalf("Telegram HTML in user name was not escaped: %q", got)
	}
}

func TestComparisonSelectionTogglesAndCapsAtThree(t *testing.T) {
	b := &Bot{}
	for i, id := range []string{"001", "002", "003"} {
		count, full := b.toggleComparisonSelection(456, id)
		if full || count != i+1 {
			t.Fatalf("selection %s: count=%d full=%v", id, count, full)
		}
	}
	if count, full := b.toggleComparisonSelection(456, "004"); !full || count != 3 {
		t.Fatalf("fourth station should be rejected: count=%d full=%v", count, full)
	}
	if count, full := b.toggleComparisonSelection(456, "002"); full || count != 2 {
		t.Fatalf("toggling a selected station should remove it: count=%d full=%v", count, full)
	}
	if got := b.comparisonSelection(456); !reflect.DeepEqual(got, []string{"001", "003"}) {
		t.Fatalf("unexpected comparison selection: %v", got)
	}
}
func TestCardEmojiFitsTelegramLimit(t *testing.T) {
	l := catalog.Location{Title: strings.Repeat("🚇", 100), Rating: strings.Repeat("⭐", 80), Landmarks: strings.Repeat("📍", 160), Description: strings.Repeat("🔎", 7000)}
	card := cardForLocation(l, "https://yandex.ru/maps/?um=constructor%3Atest")
	display := html.UnescapeString(strings.ReplaceAll(strings.ReplaceAll(card, "<b>", ""), "</b>", ""))
	if n := utf16Len(display); n > 4096 {
		t.Fatalf("emoji text too long: %d", n)
	}
}

// The user-supplied copy for station 002 is stored as structured data, not
// inferred from a photo or fabricated for other stations.
func TestKlenovyBulvarSourceAnd39BlankTemplates(t *testing.T) {
	data, err := os.ReadFile("../../config/partner_cards.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries map[string]map[string]string
	if err = json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 40 {
		t.Fatalf("expected 40 templates; got %d", len(entries))
	}
	source, ok := entries["002"]
	if !ok {
		t.Fatal("missing Кленовый бульвар")
	}
	expected := map[string]string{
		"line_status": "Бирюлёвская линия · открыто",
		"landmarks":   "Нагатино-Садовники",
		"traffic":     "Действующая станция: пассажиропоток идёт уже сегодня, ждать пуска не нужно.",
		"payers":      "Жители Нагатино-Садовников: срочная печать, копии, документы.",
		"opportunity": "Окно открыто прямо сейчас — старт выручки с первого месяца работы.",
		"conclusion":  "Единственная точка у работающей станции — старт без периода ожида…",
	}
	for key, value := range expected {
		if source[key] != value {
			t.Errorf("incorrect %s: %q", key, source[key])
		}
	}
	for id, entry := range entries {
		if id == "002" {
			continue
		}
		for k, v := range entry {
			if v != "" {
				t.Fatalf("unexpected invented content in %s field %s: %q", id, k, v)
			}
		}
	}
	l := catalog.Location{ID: "002", Title: "Метро Кленовый бульвар", LineStatus: source["line_status"], Landmarks: source["landmarks"], Traffic: source["traffic"], Payers: source["payers"], Opportunity: source["opportunity"], Conclusion: source["conclusion"]}
	card := cardForLocation(l, "https://yandex.ru/maps/?um=constructor%3Amap")
	for _, text := range []string{"КОПИРКА · КАРТА ЛОКАЦИЙ ДЛЯ ПАРТНЁРОВ", "Нагатино-Садовники", "1. ТРАФИК И ЛОКАЦИЯ", "2. КТО ПЛАТИТ", "3. ОКНО ВОЗМОЖНОСТЕЙ", "4. ИТОГ: ПОЧЕМУ ИМЕННО ЗДЕСЬ", "Окно открыто прямо сейчас", "Единственная точка"} {
		if !strings.Contains(card, text) {
			t.Errorf("missing %q in %s", text, card)
		}
	}
	if strings.Contains(card, "Оценка:") {
		t.Fatal("rating not provided, should not be invented")
	}
	if !strings.Contains(card, "<a href=\"https://yandex.ru/maps/?um=constructor%3Amap\">Открыть карту</a>") {
		t.Fatal("project map fallback link missing")
	}
}
