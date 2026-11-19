package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// These six curated locations were withdrawn from the client catalogue
// on 2026-11-19. This migration runs once per runtime volume and uses
// the existing reversible deletion ledger rather than destroying cards.
// Do not use title substring matching: "Красногорская" and "Красногорск 5"
// are separate locations and must stay visible.
var requestedCatalogRetirements20261119 = []string{
	"005", // Метро Кавказский бульвар
	"008", // Метро Лебедянская
	"009", // Метро Бирюлёво
	"084", // Красногорск (the "Красногорск 2029" listing)
	"087", // Гражданская
	"114", // Сапроново
}

const requestedCatalogRetirementsMarker = "catalog_retirements_20261119.applied"

// applyRequestedCatalogRetirements merges withdrawn IDs with any previous
// admin deletions. The marker is intentionally written AFTER the ledger,
// so interruptions cannot accidentally expose a withdrawn location.
// Once applied, admins can restore individual locations as usual.
func (s *Store) applyRequestedCatalogRetirements() error {
	marker := filepath.Join(s.dataDir, requestedCatalogRetirementsMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read catalog retirements marker: %w", err)
	}

	path := s.DeletedLocationsPath()
	deleted, err := ReadDeletedLocations(path)
	if err != nil {
		return err
	}
	for _, id := range requestedCatalogRetirements20261119 {
		deleted[id] = true
	}
	if err := ValidateDeletedLocations(deleted); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(deleted, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateAtomic(path, contents); err != nil {
		return fmt.Errorf("persist withdrawn locations: %w", err)
	}
	if err := writePrivateAtomic(marker, []byte("applied\n")); err != nil {
		return fmt.Errorf("persist catalog retirements marker: %w", err)
	}
	return nil
}
