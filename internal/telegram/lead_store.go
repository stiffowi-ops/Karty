package telegram

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type leadDraft struct {
	Source      string `json:"source"`
	SuggestedID string `json:"suggested_id,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Company     string `json:"company,omitempty"`
	Name        string `json:"name,omitempty"`
	Phone       string `json:"phone,omitempty"`
	LocationID  string `json:"location_id,omitempty"`
	Stage       string `json:"stage"`
	Page        int    `json:"page,omitempty"`
}

type leadApplication struct {
	Number       int64           `json:"number"`
	UserID       int64           `json:"user_id"`
	Username     string          `json:"username,omitempty"`
	Kind         string          `json:"kind"`
	Company      string          `json:"company,omitempty"`
	Name         string          `json:"name"`
	Phone        string          `json:"phone"`
	LocationID   string          `json:"location_id"`
	LocationName string          `json:"location_name"`
	Source       string          `json:"source"`
	CreatedAt    time.Time       `json:"created_at"`
	Delivered    map[string]bool `json:"delivered,omitempty"`
}

type leadState struct {
	Drafts    map[string]leadDraft       `json:"drafts"`
	Submitted map[string]leadApplication `json:"submitted"`
	NextID    int64                      `json:"next_id"`
}

type leadStore struct {
	mu    sync.Mutex
	path  string
	state leadState
}

func openLeadStore(path string) (*leadStore, error) {
	s := &leadStore{path: path, state: leadState{Drafts: map[string]leadDraft{}, Submitted: map[string]leadApplication{}}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read lead store: %w", err)
	}
	if err := json.Unmarshal(raw, &s.state); err != nil {
		return nil, fmt.Errorf("decode lead store: %w", err)
	}
	if s.state.Drafts == nil {
		s.state.Drafts = map[string]leadDraft{}
	}
	if s.state.Submitted == nil {
		s.state.Submitted = map[string]leadApplication{}
	}
	return s, nil
}

func (s *leadStore) draft(userID int64) (leadDraft, bool) {
	if s == nil || userID <= 0 {
		return leadDraft{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.state.Drafts[strconv.FormatInt(userID, 10)]
	return d, ok
}

func (s *leadStore) submitted(userID int64) (leadApplication, bool) {
	if s == nil || userID <= 0 {
		return leadApplication{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.state.Submitted[strconv.FormatInt(userID, 10)]
	return l, ok
}

func (s *leadStore) cloneLocked() leadState {
	next := leadState{Drafts: make(map[string]leadDraft, len(s.state.Drafts)), Submitted: make(map[string]leadApplication, len(s.state.Submitted)), NextID: s.state.NextID}
	for key, draft := range s.state.Drafts {
		next.Drafts[key] = draft
	}
	for key, lead := range s.state.Submitted {
		copyLead := lead
		copyLead.Delivered = make(map[string]bool, len(lead.Delivered))
		for admin, delivered := range lead.Delivered {
			copyLead.Delivered[admin] = delivered
		}
		next.Submitted[key] = copyLead
	}
	return next
}

func (s *leadStore) saveLocked(next leadState) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".lead-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(content)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *leadStore) putDraft(userID int64, d leadDraft) error {
	if s == nil || userID <= 0 {
		return errors.New("invalid lead draft")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatInt(userID, 10)
	if _, sent := s.state.Submitted[key]; sent {
		return errors.New("lead already submitted")
	}
	next := s.cloneLocked()
	next.Drafts[key] = d
	return s.saveLocked(next)
}

func (s *leadStore) clearDraft(userID int64) error {
	if s == nil || userID <= 0 {
		return errors.New("invalid user")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cloneLocked()
	delete(next.Drafts, strconv.FormatInt(userID, 10))
	return s.saveLocked(next)
}

// submit is atomic under the store lock: two simultaneous clicks cannot
// create two applications, and the draft disappears only after disk commit.
func (s *leadStore) submit(userID int64, username, locationName string) (leadApplication, bool, error) {
	if s == nil || userID <= 0 {
		return leadApplication{}, false, errors.New("invalid user")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strconv.FormatInt(userID, 10)
	if previous, exists := s.state.Submitted[key]; exists {
		return previous, true, nil
	}
	d, ok := s.state.Drafts[key]
	if !ok || d.Kind == "" || d.Name == "" || d.Phone == "" || d.LocationID == "" || (d.Kind == "new" && d.Company == "") {
		return leadApplication{}, false, errors.New("incomplete application")
	}
	next := s.cloneLocked()
	next.NextID++
	lead := leadApplication{
		Number: next.NextID, UserID: userID, Username: username,
		Kind: d.Kind, Company: d.Company, Name: d.Name, Phone: d.Phone,
		LocationID: d.LocationID, LocationName: locationName, Source: d.Source,
		CreatedAt: time.Now().UTC(), Delivered: map[string]bool{},
	}
	next.Submitted[key] = lead
	delete(next.Drafts, key)
	if err := s.saveLocked(next); err != nil {
		return leadApplication{}, false, err
	}
	return lead, false, nil
}

func (s *leadStore) markDelivered(userID, adminID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cloneLocked()
	key := strconv.FormatInt(userID, 10)
	lead, ok := next.Submitted[key]
	if !ok {
		return errors.New("lead missing")
	}
	if lead.Delivered == nil {
		lead.Delivered = map[string]bool{}
	}
	lead.Delivered[strconv.FormatInt(adminID, 10)] = true
	next.Submitted[key] = lead
	return s.saveLocked(next)
}

func (s *leadStore) allSubmitted() []leadApplication {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]leadApplication, 0, len(s.state.Submitted))
	for _, lead := range s.state.Submitted {
		out = append(out, lead)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out
}