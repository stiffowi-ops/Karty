package syncer

import (
	"context"
	"encoding/json"
	"io"
	"locationbot/internal/catalog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitleAndIDs(t *testing.T) {
	tests := []struct{ name, id, title string }{
		{"001 Метро Остров Мечты.png", "001", "Метро Остров Мечты"},
		{"040 Королёв 3.png", "040", "Королёв 3"},
	}
	for _, tt := range tests {
		if id := locationID(tt.name); id != tt.id {
			t.Errorf("id %q != %q", id, tt.id)
		}
		if title := Title(tt.name); title != tt.title {
			t.Errorf("title %q != %q", title, tt.title)
		}
	}
}

// The catalog is rebuilt on every Disk sync. Per-station links and landmarks
// must not disappear when the image has not changed.
func TestSyncPreservesLandmarksAndPerPointURL(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.json")
	expectedLink := "https://yandex.ru/maps/?whatshere%5Bpoint%5D=37.62%2C55.75"
	original := []catalog.Location{{ID: "017", Title: "Метро Троицк", Filename: "017 Метро Троицк.png", ImageExt: ".png", Category: "Метро", Landmarks: "Рядом с кафе (проверено)", MapURL: expectedLink, SourceModified: "2026-01-01T00:00:00+00:00"}}
	contents, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(seed, contents, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := catalog.New(filepath.Join(root, "runtime"), seed, filepath.Join(root, "overrides.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(store.DataDir(), "images", "017.png"), []byte("photo"), 0600); err != nil {
		t.Fatal(err)
	}
	service := New(store, "https://disk.yandex.ru/d/demo")
	service.client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		payload := `{"_embedded":{"items":[{"name":"017 Метро Троицк.png","type":"file","modified":"2026-01-01T00:00:00+00:00","size":5}]}}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})}
	result, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 {
		t.Fatalf("cached image not reused: %+v", result)
	}
	got, ok := store.Find("017")
	if !ok || got.MapURL != expectedLink || got.Landmarks != "Рядом с кафе (проверено)" {
		t.Fatalf("sync lost station data: %+v", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
