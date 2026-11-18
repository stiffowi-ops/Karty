package telegram

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestFavoriteStorePersistsPerTelegramUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "favorites.json")
	store, err := openFavoriteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := store.Toggle(101, "002"); err != nil || !added {
		t.Fatalf("first toggle should add the station: added=%v err=%v", added, err)
	}
	if added, err := store.Toggle(202, "003"); err != nil || !added {
		t.Fatalf("second user should add their station: added=%v err=%v", added, err)
	}
	if got := store.IDs(101); !reflect.DeepEqual(got, []string{"002"}) {
		t.Fatalf("user 101 favorites: %v", got)
	}
	if got := store.IDs(202); !reflect.DeepEqual(got, []string{"003"}) {
		t.Fatalf("user 202 favorites: %v", got)
	}

	reloaded, err := openFavoriteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.IDs(101); !reflect.DeepEqual(got, []string{"002"}) {
		t.Fatalf("favorites did not survive reload: %v", got)
	}
	if _, err := reloaded.Remove(101, "002"); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.IDs(101); len(got) != 0 {
		t.Fatalf("removed favorite still present: %v", got)
	}
	if got := reloaded.IDs(202); !reflect.DeepEqual(got, []string{"003"}) {
		t.Fatalf("removing one user's station affected another user: %v", got)
	}
}
