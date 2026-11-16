package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	if strings.Contains(text, `<a href=`) {
		t.Fatal("unverified link was displayed")
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
	if got := stationMapLink(catalog.Location{ID: "003"}, base); got != "" {
		t.Fatalf("map link invented without coordinates: %q", got)
	}
	direct := "https://yandex.ru/maps/?whatshere%5Bpoint%5D=37.7%2C55.7"
	if got := stationMapLink(catalog.Location{MapURL: direct}, base); got != direct {
		t.Fatalf("exact source link not preferred: %q", got)
	}
	if got := stationMapLink(catalog.Location{MapURL: "https://evil.test/steal"}, base); got != "" {
		t.Fatalf("unsafe url allowed: %q", got)
	}
	card := cardForLocation(a, base)
	if !strings.Contains(card, "Открыть точку в Яндекс Картах") || !strings.Contains(card, "<a href=") {
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
	album    []byte
	card     map[string]any
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req.URL.Path)
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	switch req.URL.Path {
	case "/botTEST/sendMediaGroup":
		c.album = raw
	case "/botTEST/sendMessage":
		if err = json.Unmarshal(raw, &c.card); err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{}}`)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
}
func TestThreeOriginalPhotosThenCardWithLinks(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed.json")
	if err := os.WriteFile(seed, []byte(`[{"id":"017","title":"Метро Троицк","filename":"017.png","image_ext":".png","description":"Тестовое описание","rating":"8/10","lat":55.75,"lon":37.62}]`), 0600); err != nil {
		t.Fatal(err)
	}
	imageBytes := []byte("ORIGINAL RAW PNG BYTES - NO RESIZING")
	if err := os.MkdirAll(filepath.Join(dir, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "images", "017.png"), imageBytes, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.New(dir, seed, filepath.Join(dir, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	tr := &captureTransport{}
	b := &Bot{token: "TEST", mapURL: "https://yandex.ru/maps/?um=constructor%3Atest", store: store, http: &http.Client{Transport: tr}}
	if err := b.showLocation(context.Background(), 123, "017", 2); err != nil {
		t.Fatal(err)
	}
	if len(tr.requests) != 2 || tr.requests[0] != "/botTEST/sendMediaGroup" || tr.requests[1] != "/botTEST/sendMessage" {
		t.Fatalf("album followed by card required: %v", tr.requests)
	}
	first := bytes.SplitN(tr.album, []byte("\r\n"), 2)[0]
	boundary := strings.TrimPrefix(string(first), "--")
	mr := multipart.NewReader(bytes.NewReader(tr.album), boundary)
	parts := map[string][]byte{}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(p)
		if err != nil {
			t.Fatal(err)
		}
		parts[p.FormName()] = content
	}
	for i := 0; i < 3; i++ {
		name := "photo" + string(rune('0'+i))
		if !bytes.Equal(parts[name], imageBytes) {
			t.Fatalf("%s differs from original image", name)
		}
	}
	var media []map[string]string
	if err := json.Unmarshal(parts["media"], &media); err != nil {
		t.Fatal(err)
	}
	if len(media) != 3 {
		t.Fatalf("exactly 3 photos required: %+v", media)
	}
	for i, m := range media {
		if m["media"] != "attach://photo"+string(rune('0'+i)) {
			t.Fatalf("incorrect photo mapping: %+v", media)
		}
	}
	text, ok := tr.card["text"].(string)
	if !ok || !strings.Contains(text, "Тестовое описание") || !strings.Contains(text, "Открыть точку в Яндекс Картах") {
		t.Fatalf("station text or unique map URL missing: %+v", tr.card)
	}
	kb, ok := tr.card["reply_markup"].(map[string]any)
	if !ok {
		t.Fatalf("missing keyboard: %+v", tr.card)
	}
	keyboardBytes, _ := json.Marshal(kb)
	if !bytes.Contains(keyboardBytes, []byte("page:2")) || !bytes.Contains(keyboardBytes, []byte("search")) || !bytes.Contains(keyboardBytes, []byte("menu")) {
		t.Fatalf("missing nav buttons: %s", keyboardBytes)
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
	if strings.Contains(card, `<a href=`) {
		t.Fatal("no verified map point yet")
	}
}
