package telegram

import (
	"encoding/json"
	"math"
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
