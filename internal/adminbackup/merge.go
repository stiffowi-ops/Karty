package adminbackup

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"locationbot/internal/analytics"
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
