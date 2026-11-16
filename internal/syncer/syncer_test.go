package syncer

import "testing"

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
