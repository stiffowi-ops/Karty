package adminbackup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"locationbot/internal/catalog"
)

const MaxZIP = 15 << 20
const MaxUncompressed = 35 << 20
const Schema = 1
const manifestFile = "manifest.json"
const effectiveFile = "catalog_effective.json"
const cardsFile = "admin_cards.json"

var optionalFiles = []string{"leads.json", "favorites.json", "analytics.jsonl", "admin_history.json", cardsFile}

type Manifest struct {
	Schema    int               `json:"schema"`
	CreatedAt time.Time         `json:"created_at"`
	Files     map[string]string `json:"sha256"`
}

type Inspection struct {
	CreatedAt time.Time
	Locations int
	Leads     int
	Favorites int
	Files     []string
}

type Archive struct {
	Contents map[string][]byte
	Inspect  Inspection
}

func acceptName(name string) bool {
	if name == effectiveFile || name == cardsFile {
		return true
	}
	for _, f := range optionalFiles {
		if name == f {
			return true
		}
	}
	return false
}

func copyBytes(v []byte) []byte { return append([]byte(nil), v...) }

func Build(root string, effective []catalog.Location) ([]byte, error) {
	files := map[string][]byte{}
	catalogData, err := json.MarshalIndent(effective, "", "  ")
	if err != nil {
		return nil, err
	}
	files[effectiveFile] = catalogData
	for _, name := range optionalFiles {
		b, e := os.ReadFile(filepath.Join(root, name))
		if errors.Is(e, os.ErrNotExist) {
			switch name {
			case "analytics.jsonl":
				b = []byte{}
			case "leads.json":
				b = []byte(`{"drafts":{},"submitted":{},"next_id":0}`)
			case "admin_history.json":
				b = []byte(`[]`)
			default:
				b = []byte(`{}`)
			}
		} else if e != nil {
			return nil, e
		}
		files[name] = b
	}
	manifest := Manifest{Schema: Schema, CreatedAt: time.Now().UTC(), Files: map[string]string{}}
	for name, contents := range files {
		sum := sha256.Sum256(contents)
		manifest.Files[name] = hex.EncodeToString(sum[:])
	}
	meta, _ := json.MarshalIndent(manifest, "", "  ")
	files[manifestFile] = meta
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetModTime(manifest.CreatedAt)
		h.SetMode(0600)
		w, e := writer.CreateHeader(h)
		if e != nil {
			return nil, e
		}
		if _, e = w.Write(files[name]); e != nil {
			return nil, e
		}
	}
	if e := writer.Close(); e != nil {
		return nil, e
	}
	if out.Len() > MaxZIP {
		return nil, errors.New("резервная копия слишком большая для загрузки в Telegram")
	}
	return out.Bytes(), nil
}

func Examine(raw []byte) (Archive, error) {
	if len(raw) == 0 || len(raw) > MaxZIP {
		return Archive{}, errors.New("неподдерживаемый размер архива")
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return Archive{}, err
	}
	files := map[string][]byte{}
	total := int64(0)
	for _, f := range zr.File {
		if f.FileInfo().Mode()&os.ModeSymlink != 0 || strings.Contains(f.Name, "/") || !acceptName(f.Name) && f.Name != manifestFile {
			return Archive{}, fmt.Errorf("недопустимый файл в ZIP: %s", f.Name)
		}
		if _, exists := files[f.Name]; exists {
			return Archive{}, errors.New("повторяющиеся файлы в ZIP")
		}
		if f.UncompressedSize64 > MaxUncompressed {
			return Archive{}, errors.New("слишком большой файл в архиве")
		}
		total += int64(f.UncompressedSize64)
		if total > MaxUncompressed {
			return Archive{}, errors.New("архив превышает лимит распаковки")
		}
		rd, e := f.Open()
		if e != nil {
			return Archive{}, e
		}
		b, e := io.ReadAll(io.LimitReader(rd, MaxUncompressed+1))
		rd.Close()
		if e != nil {
			return Archive{}, e
		}
		files[f.Name] = b
	}
	var manifest Manifest
	if err = json.Unmarshal(files[manifestFile], &manifest); err != nil {
		return Archive{}, fmt.Errorf("некорректное описание бэкапа: %w", err)
	}
	if manifest.Schema != Schema || len(manifest.Files) != len(files)-1 {
		return Archive{}, errors.New("неподдерживаемая версия бэкапа или неполный список файлов")
	}
	for name, expected := range manifest.Files {
		data, exists := files[name]
		if !exists || !acceptName(name) {
			return Archive{}, fmt.Errorf("нет ожидаемого файла %s", name)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			return Archive{}, fmt.Errorf("нарушена целостность %s", name)
		}
	}
	if _, ok := files[effectiveFile]; !ok {
		return Archive{}, errors.New("отсутствует каталог локаций")
	}
	var locations []catalog.Location
	if err = json.Unmarshal(files[effectiveFile], &locations); err != nil {
		return Archive{}, err
	}
	if len(locations) == 0 || len(locations) > 1000 {
		return Archive{}, errors.New("неверное число локаций")
	}
	seen := map[string]bool{}
	for _, l := range locations {
		if !catalog.ValidID(l.ID) || seen[l.ID] {
			return Archive{}, errors.New("повторяющиеся или неверные идентификаторы локаций")
		}
		seen[l.ID] = true
		if err = catalog.Revision(l).Validate(); err != nil {
			return Archive{}, err
		}
	}
	if _, err := parseLeads(files["leads.json"]); err != nil {
		return Archive{}, fmt.Errorf("invalid applications: %w", err)
	}
	if _, err := mergeAnalytics(files["analytics.jsonl"], nil); err != nil {
		return Archive{}, fmt.Errorf("invalid analytics: %w", err)
	}
	for _, mandatory := range []string{"leads.json", "favorites.json", "admin_history.json", "analytics.jsonl", "admin_cards.json"} {
		if _, ok := files[mandatory]; !ok {
			return Archive{}, fmt.Errorf("missing mandatory backup file %s", mandatory)
		}
	}
	var previous map[string]catalog.CardRevision
	if err := json.Unmarshal(files["admin_cards.json"], &previous); err != nil {
		return Archive{}, err
	}
	for id, revision := range previous {
		if !catalog.ValidID(id) {
			return Archive{}, errors.New("invalid admin card ID")
		}
		if err := revision.Validate(); err != nil {
			return Archive{}, err
		}
	}
	var log []json.RawMessage
	if err := json.Unmarshal(files["admin_history.json"], &log); err != nil {
		return Archive{}, err
	}
	var favorites map[string][]string
	if len(files["favorites.json"]) > 0 {
		if err = json.Unmarshal(files["favorites.json"], &favorites); err != nil {
			return Archive{}, err
		}
	}
	var lead struct {
		Submitted map[string]json.RawMessage `json:"submitted"`
	}
	if len(files["leads.json"]) > 0 {
		if err = json.Unmarshal(files["leads.json"], &lead); err != nil {
			return Archive{}, err
		}
	}
	result := Inspection{CreatedAt: manifest.CreatedAt, Locations: len(locations), Leads: len(lead.Submitted)}
	for userID, ids := range favorites {
		if !digitsOnly(userID) {
			return Archive{}, errors.New("неверный Telegram ID в избранном")
		}
		unique := map[string]bool{}
		for _, id := range ids {
			if !seen[id] || unique[id] {
				return Archive{}, errors.New("неверная или дублирующаяся локация в избранном")
			}
			unique[id] = true
		}
		result.Favorites += len(ids)
	}
	for name := range manifest.Files {
		result.Files = append(result.Files, name)
	}
	sort.Strings(result.Files)
	return Archive{Contents: files, Inspect: result}, nil
}

// Restore writes to a private runtime directory, never config or secrets.
// It first stores a protective snapshot and rolls back any failed file writes.
func Restore(root string, archive Archive, mode string, current []catalog.Location) (string, error) {
	if mode != "cards" && mode != "full" {
		return "", errors.New("неверный режим восстановления")
	}
	if err := os.MkdirAll(filepath.Join(root, "backups"), 0700); err != nil {
		return "", err
	}
	protective, err := Build(root, current)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "backups", "pre-restore-"+time.Now().UTC().Format("20060102-150405.000000000")+".zip")
	if err = atomicWrite(path, protective); err != nil {
		return "", err
	}
	// The effective catalog contains source descriptions even if the admin_cards
	// file in the archive is empty. Rebuild the override layer from each card.
	var locations []catalog.Location
	if err = json.Unmarshal(archive.Contents[effectiveFile], &locations); err != nil {
		return path, err
	}
	revisions := map[string]catalog.CardRevision{}
	currentIDs := map[string]bool{}
	for _, loc := range current {
		currentIDs[loc.ID] = true
	}
	if len(currentIDs) != len(locations) {
		return path, errors.New("архив содержит другое число локаций: восстановление запрещено")
	}
	for _, loc := range locations {
		if !currentIDs[loc.ID] {
			return path, fmt.Errorf("локация %s отсутствует в текущем каталоге", loc.ID)
		}
		revisions[loc.ID] = catalog.Revision(loc)
	}
	override, _ := json.MarshalIndent(revisions, "", "  ")
	replacement := map[string][]byte{cardsFile: override}
	if mode == "full" {
		for _, name := range []string{"leads.json", "favorites.json", "analytics.jsonl", "admin_history.json"} {
			replacement[name] = copyBytes(archive.Contents[name])
		}
		currentLeads, err := os.ReadFile(filepath.Join(root, "leads.json"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return path, err
		}
		protectedLeads, err := mergeLeads(replacement["leads.json"], currentLeads)
		if err != nil {
			return path, err
		}
		replacement["leads.json"] = protectedLeads
		currentAnalytics, err := os.ReadFile(filepath.Join(root, "analytics.jsonl"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return path, err
		}
		merged, err := mergeAnalytics(replacement["analytics.jsonl"], currentAnalytics)
		if err != nil {
			return path, err
		}
		replacement["analytics.jsonl"] = merged
	}
	old := map[string][]byte{}
	existed := map[string]bool{}
	names := make([]string, 0, len(replacement))
	for name := range replacement {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		val, e := os.ReadFile(filepath.Join(root, name))
		if e == nil {
			old[name] = val
			existed[name] = true
		} else if !errors.Is(e, os.ErrNotExist) {
			return path, e
		}
	}
	changed := []string{}
	for _, name := range names {
		if e := atomicWrite(filepath.Join(root, name), replacement[name]); e != nil {
			for i := len(changed) - 1; i >= 0; i-- {
				n := changed[i]
				if existed[n] {
					_ = atomicWrite(filepath.Join(root, n), old[n])
				} else {
					_ = os.Remove(filepath.Join(root, n))
				}
			}
			return path, fmt.Errorf("восстановление отменено: %w", e)
		}
		changed = append(changed, name)
	}
	return path, nil
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".karty-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if c := file.Close(); err == nil {
		err = c
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
