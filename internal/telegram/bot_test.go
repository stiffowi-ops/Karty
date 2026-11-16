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


func TestCatalogPageUsesFriendlyLocationIntroduction(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	stations := make([]catalog.Location, 0, 11)
	for _, id := range []string{"001", "002", "003", "004", "005", "006", "007", "008", "009", "010", "011"} {
		stations = append(stations, catalog.Location{ID: id, Title: "Локация " + id})
	}
	raw, err := json.Marshal(stations)
	if err != nil { t.Fatal(err) }
	if err := os.WriteFile(seed, raw, 0600); err != nil { t.Fatal(err) }
	store, err := catalog.New(filepath.Join(root, "runtime"), seed, filepath.Join(root, "overrides.json"))
	if err != nil { t.Fatal(err) }
	tr := &captureTransport{}
	b := &Bot{token: "TEST", store: store, http: &http.Client{Transport: tr}}
	for _, tc := range []struct{ page int; expected string }{
		{0, "📖 Страница 1 из 2"},
		{1, "📖 Страница 2 из 2"},
		{10, "📖 Страница 2 из 2"},
		{-1, "📖 Страница 1 из 2"},
	} {
		if err := b.showPage(context.Background(), 123, tc.page); err != nil { t.Fatal(err) }
		text, _ := tr.card["text"].(string)
		for _, want := range []string{
			"<b>📍 Каталог локаций</b>",
			"Здесь собраны 11 точек с подробным описанием и возможностями для бизнеса.",
			"сохраняй понравившиеся варианты в избранное ⭐",
			tc.expected,
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("page %d: expected %q in %q", tc.page, want, text)
			}
		}
		if strings.Contains(text, "Станции от А до Я") || strings.Contains(text, "объектов") {
			t.Fatalf("old dry catalog heading remains: %s", text)
		}
	}
}

func TestLocationsButtonAndLegacyTextOpenCatalog(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"001","title":"Метро Остров Мечты"}]`), 0600); err != nil { t.Fatal(err) }
	store, err := catalog.New(filepath.Join(root, "runtime"), seed, filepath.Join(root, "overrides.json"))
	if err != nil { t.Fatal(err) }
	tr := &captureTransport{}
	b := &Bot{token:"TEST", store:store, http:&http.Client{Transport:tr}}
	for _, label := range []string{"📋 Список локаций", "📋 Список станций"} {
		msg := &message{Text:label, Chat:chat{ID:123,Type:"private"}, From:user{ID:456}}
		if err := b.handle(context.Background(), update{Message:msg}); err != nil { t.Fatal(err) }
		if got, _ := tr.card["text"].(string); !strings.Contains(got,"📍 Каталог локаций") {
			t.Fatalf("button %q did not open catalog: %v",label,got)
		}
	}
}


func TestWelcomeReflectsActualCatalogSize(t *testing.T) {
	for _, tc := range []struct{ count int; want string }{{114, "114 локаций"}, {116, "116 локаций"}} {
		text := welcomeText(user{}, tc.count)
		if !strings.Contains(text, tc.want) { t.Fatalf("expected %q, got %q", tc.want, text) }
	}
}

func TestWelcomeUsesBusinessGrowthCopy(t *testing.T) {
	text := welcomeText(user{FirstName: "Иван"}, 114)
	for _, expected := range []string{
		"Привет, Иван! 👋",
		"<b>Не просто точки на карте. Точки роста твоего бизнеса!</b>",
		"114 локаций с интересными возможностями для открытия и развития бизнеса",
		"потенциальную аудиторию",
		"сохраняй лучшие в избранное ⭐",
		"<b>Найди свою следующую точку роста 👇</b>",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("welcome message is missing %q: %s", expected, text)
		}
	}
	if strings.Contains(text, "Ближайшие точки по геопозиции") || strings.Contains(text, "Я помогу найти станцию") {
		t.Fatal("outdated greeting text was not removed")
	}
}

func TestStartCommandSendsWelcomeWithInlineMenuAndNearLast(t *testing.T) {
	tr := &captureTransport{}
	b := &Bot{token: "TEST", mapURL: defaultProjectMapURL, http: &http.Client{Transport: tr}}
	msg := &message{
		Text: "/start",
		Chat: chat{ID: 123, Type: "private"},
		From: user{ID: 456, FirstName: "Иван", LastName: "Иванов"},
	}
	if err := b.handle(context.Background(), update{Message: msg}); err != nil {
		t.Fatal(err)
	}
	if len(tr.payloads) != 2 {
		t.Fatalf("expected temporary keyboard reset and welcome, got %d messages", len(tr.payloads))
	}
	reset, _ := tr.payloads[0]["reply_markup"].(map[string]any)
	if reset["remove_keyboard"] != true {
		t.Fatalf("old persistent keyboard not removed: %+v", reset)
	}
	welcome := tr.payloads[1]
	welcomeText, _ := welcome["text"].(string)
	if !strings.Contains(welcomeText, "Привет, Иван Иванов!") {
		t.Fatalf("personalized welcome missing: %+v", welcome["text"])
	}
	markup, _ := welcome["reply_markup"].(map[string]any)
	if markup["keyboard"] != nil || markup["remove_keyboard"] != nil {
		t.Fatalf("welcome should use inline-only buttons: %+v", markup)
	}
	rows, ok := markup["inline_keyboard"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("expected 3 rows in inline menu: %+v", markup)
	}
	assertButton := func(row, column int, label, dataType, data string) {
		t.Helper()
		buttons, ok := rows[row].([]any)
		if !ok || column >= len(buttons) {
			t.Fatalf("missing button at row %d, column %d", row, column)
		}
		button, ok := buttons[column].(map[string]any)
		if !ok || button["text"] != label || button[dataType] != data {
			t.Fatalf("wrong menu button: %+v", button)
		}
	}
	assertButton(0, 0, "📋 Список локаций", "callback_data", "list")
	assertButton(0, 1, "🔎 Поиск", "callback_data", "search")
	assertButton(1, 0, "⭐ Избранное", "callback_data", "favorites")
	assertButton(1, 1, "🗺 Открыть карту", "url", defaultProjectMapURL)
	assertButton(2, 0, "📍 Рядом со мной", "callback_data", "near")
	if lastRow, _ := rows[2].([]any); len(lastRow) != 1 {
		t.Fatalf("nearby button should be alone in last row: %+v", lastRow)
	}
	if len(tr.requests) != 3 || tr.requests[2] != "/botTEST/deleteMessage" {
		t.Fatalf("temporary keyboard-reset message not cleaned up: %+v", tr.requests)
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

func TestLocationRequestAndReplyKeyboardRemoval(t *testing.T) {
	keyboard, _ := json.Marshal(mainKeyboard())
	for _, text := range []string{"request_location", "📍 Отправить геопозицию", "🏠 Главное меню", "one_time_keyboard"} {
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
		t.Fatalf("expected keyboard removal and nearby results, got %d messages", len(tr.payloads))
	}
	markup, _ := tr.payloads[0]["reply_markup"].(map[string]any)
	if markup["remove_keyboard"] != true {
		t.Fatalf("location response must hide native keyboard, got %+v", markup)
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
	if !strings.Contains(text, "Пока здесь пусто — сохраним первую локацию?") || !strings.Contains(text, "Сохранённые локации появятся здесь") {
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
	if got := welcomeText(user{}, 114); !strings.HasPrefix(got, "Привет! 👋") {
		t.Fatalf("missing generic greeting fallback: %q", got)
	}
	if got := welcomeText(user{FirstName: "<Иван>"}, 114); !strings.Contains(got, "Привет, &lt;Иван&gt;! 👋") {
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

// The curated catalog contains all 114 station/location descriptions.
func TestKlenovyBulvarCuratedCard(t *testing.T) {
	root := t.TempDir()
	seedPath := filepath.Join(root, "seed.json")
	if err := os.WriteFile(seedPath, []byte(`[{"id":"002","title":"Метро Кленовый бульвар"}]`), 0600); err != nil { t.Fatal(err) }
	store, err := catalog.NewWithBundled(filepath.Join(root,"runtime"), seedPath, filepath.Join(root,"overrides.json"))
	if err != nil {t.Fatal(err)}
	l, ok := store.Find("002")
	if !ok {t.Fatal("Кленовый бульвар missing")}
	card := cardForLocation(l, "https://yandex.ru/maps/?um=constructor%3Amap")
	for _, value := range []string{"<b>Метро Кленовый бульвар</b>", "Нагатино-Садовники", "1. ТРАФИК И ЛОКАЦИЯ", "2. КТО ПЛАТИТ", "3. ОКНО ВОЗМОЖНОСТЕЙ", "4. ИТОГ: ПОЧЕМУ ИМЕННО ЗДЕСЬ", "без периода ожидания"} {
		if !strings.Contains(card, value) { t.Fatalf("card missing %q",value) }
	}
	if strings.Contains(card, "КОПИРКА · КАРТА ЛОКАЦИЙ ДЛЯ ПАРТНЁРОВ") { t.Fatal("obsolete banner must be absent") }
}
