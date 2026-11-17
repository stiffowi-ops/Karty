package adminbackup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"locationbot/internal/analytics"
	"locationbot/internal/catalog"
	"sort"
	"strings"
	"time"
)

type leadSnapshot struct {
	Drafts    map[string]json.RawMessage `json:"drafts"`
	Submitted map[string]json.RawMessage `json:"submitted"`
	NextID    int64                      `json:"next_id"`
}

func parseLeads(raw []byte) (leadSnapshot, error) {
	var result leadSnapshot
	if len(raw) == 0 {
		raw = []byte(`{"drafts":{},"submitted":{},"next_id":0}`)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, err
	}
	if result.Submitted == nil {
		result.Submitted = map[string]json.RawMessage{}
	}
	if result.Drafts == nil {
		result.Drafts = map[string]json.RawMessage{}
	}
	if result.NextID < 0 {
		return result, fmt.Errorf("negative application counter")
	}
	for user, data := range result.Submitted {
		var details struct {
			Number int64 `json:"number"`
			UserID int64 `json:"user_id"`
		}
		if err := json.Unmarshal(data, &details); err != nil {
			return result, err
		}
		if !digitsOnly(user) || details.Number <= 0 {
			return result, fmt.Errorf("invalid application %s", user)
		}
		if details.Number > result.NextID {
			return result, fmt.Errorf("application counter is lower than stored number")
		}
	}
	for user := range result.Drafts {
		if !digitsOnly(user) {
			return result, fmt.Errorf("invalid draft user ID")
		}
	}
	return result, nil
}
func digitsOnly(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func mergeLeads(archived, current []byte) ([]byte, error) {
	old, err := parseLeads(archived)
	if err != nil {
		return nil, err
	}
	now, err := parseLeads(current)
	if err != nil {
		return nil, err
	}
	if old.NextID < now.NextID {
		old.NextID = now.NextID
	}
	used := map[int64]bool{}
	for user, raw := range now.Submitted {
		old.Submitted[user] = raw
		var x struct {
			Number int64 `json:"number"`
		}
		_ = json.Unmarshal(raw, &x)
		used[x.Number] = true
	}
	for user, raw := range old.Submitted {
		if _, exists := now.Submitted[user]; exists {
			continue
		}
		var x map[string]any
		if err := json.Unmarshal(raw, &x); err != nil {
			return nil, err
		}
		number := int64(x["number"].(float64))
		if used[number] {
			old.NextID++
			number = old.NextID
			x["number"] = number
			raw, _ = json.Marshal(x)
			old.Submitted[user] = raw
		}
		used[number] = true
	}
	for user, draft := range now.Drafts {
		old.Drafts[user] = draft
	}
	for user := range old.Submitted {
		delete(old.Drafts, user)
	}
	return json.MarshalIndent(old, "", "  ")
}

func mergeAnalytics(archived, current []byte) ([]byte, error) {
	events := map[string]struct{}{}
	for _, data := range [][]byte{archived, current} {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var record struct {
				At         string `json:"at"`
				Kind       string `json:"kind"`
				UserID     int64  `json:"user_id"`
				LocationID string `json:"location_id"`
			}
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				return nil, err
			}
			timestamp, err := time.Parse(time.RFC3339Nano, record.At)
			if err != nil {
				return nil, err
			}
			if err = (analytics.Event{At: timestamp, UserID: record.UserID, LocationID: record.LocationID, Kind: record.Kind}).Validate(); err != nil {
				return nil, err
			}
			events[line] = struct{}{}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	lines := make([]string, 0, len(events))
	for line := range events {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return []byte{}, nil
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// mergeFavorites protects the current users' choices when an older archive is
// restored. Archive-only users are kept unless a post-backup removal cleared
// their last favorite, which is visible in the current analytics event log.
func mergeFavorites(archived, current, events []byte, archivedAt time.Time) ([]byte, error) {
	read := func(raw []byte) (map[string][]string, error) {
		if len(bytes.TrimSpace(raw)) == 0 {
			return map[string][]string{}, nil
		}
		var data map[string][]string
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, err
		}
		if data == nil {
			data = map[string][]string{}
		}
		for user, ids := range data {
			if !digitsOnly(user) {
				return nil, fmt.Errorf("invalid favorite user ID %q", user)
			}
			seen := map[string]bool{}
			for _, id := range ids {
				if !catalog.ValidID(id) || seen[id] {
					return nil, fmt.Errorf("invalid favorite location %q", id)
				}
				seen[id] = true
			}
		}
		return data, nil
	}
	old, err := read(archived)
	if err != nil {
		return nil, err
	}
	now, err := read(current)
	if err != nil {
		return nil, err
	}
	for user, ids := range now {
		old[user] = append([]string(nil), ids...)
	}
	scanner := bufio.NewScanner(bytes.NewReader(events))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var e analytics.Event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, err
		}
		if err := e.Validate(); err != nil {
			return nil, err
		}
		if e.At.Before(archivedAt) || (e.Kind != analytics.FavoriteAdded && e.Kind != analytics.FavoriteRemoved) {
			continue
		}
		user := fmt.Sprint(e.UserID)
		if _, present := now[user]; !present {
			// Current empty state is represented by absence of a user key.
			delete(old, user)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(old, "", "  ")
}

// mergeRevisionHistory keeps newer admin audit entries when restoring an older
// archive. Every record is deduplicated by canonical JSON and sorted by time.
func mergeRevisionHistory(archived, current []byte) ([]byte, error) {
	seen := map[string]json.RawMessage{}
	for _, raw := range [][]byte{archived, current} {
		if len(bytes.TrimSpace(raw)) == 0 {
			raw = []byte("[]")
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
		for _, item := range rows {
			var check struct {
				At         time.Time `json:"at"`
				LocationID string    `json:"location_id"`
			}
			if err := json.Unmarshal(item, &check); err != nil || check.At.IsZero() || !catalog.ValidID(check.LocationID) {
				return nil, errors.New("invalid revision history record")
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, item); err != nil {
				return nil, err
			}
			seen[compact.String()] = json.RawMessage(compact.String())
		}
	}
	rows := make([]json.RawMessage, 0, len(seen))
	for _, v := range seen {
		rows = append(rows, v)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		var a, b struct {
			At time.Time `json:"at"`
		}
		_ = json.Unmarshal(rows[i], &a)
		_ = json.Unmarshal(rows[j], &b)
		return a.At.Before(b.At)
	})
	return json.MarshalIndent(rows, "", "  ")
}
