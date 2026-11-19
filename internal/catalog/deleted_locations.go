package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DeletedLocationsFilename is a persistent visibility ledger. The bundled
// catalog and custom location definitions remain intact so deletion can be
// reversed without losing card contents or historic applications.
const DeletedLocationsFilename = "deleted_locations.json"

func ReadDeletedLocations(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries := map[string]bool{}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("invalid deleted locations file: %w", err)
	}
	if err := ValidateDeletedLocations(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func ValidateDeletedLocations(entries map[string]bool) error {
	if len(entries) > 1500 {
		return errors.New("слишком много удалённых локаций")
	}
	for id, deleted := range entries {
		if !ValidID(id) || !deleted {
			return fmt.Errorf("invalid deleted location ID %q", id)
		}
	}
	return nil
}

func (s *Store) DeletedLocationsPath() string {
	return filepath.Join(s.dataDir, DeletedLocationsFilename)
}

// SetDeletedLocation removes/restores a location from the published catalog.
// It never destroys a source card or mutates existing applications.
func (s *Store) SetDeletedLocation(id string, deleted bool) error {
	if !ValidID(id) {
		return errors.New("неверный ID локации")
	}
	s.adminMu.Lock()
	defer s.adminMu.Unlock()
	entries, err := ReadDeletedLocations(s.DeletedLocationsPath())
	if err != nil {
		return err
	}
	if deleted {
		if _, found := s.Find(id); !found {
			return errors.New("локация не найдена в каталоге")
		}
		if entries[id] {
			return errors.New("локация уже удалена")
		}
		entries[id] = true
	} else {
		if !entries[id] {
			return errors.New("локация не найдена среди удалённых")
		}
		delete(entries, id)
	}
	path := s.DeletedLocationsPath()
	old, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if err := writePrivateAtomic(path, data); err != nil {
		return err
	}
	rollback := func() {
		if errors.Is(readErr, os.ErrNotExist) {
			_ = os.Remove(path)
		} else {
			_ = writePrivateAtomic(path, old)
		}
		_ = s.Reload()
	}
	if err := s.Reload(); err != nil {
		rollback()
		return fmt.Errorf("не удалось изменить доступность локации: %w", err)
	}
	if !deleted {
		if _, found := s.Find(id); !found {
			rollback()
			return errors.New("исходная карточка локации отсутствует")
		}
	}
	return nil
}
