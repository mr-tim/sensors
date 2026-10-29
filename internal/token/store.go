package token

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Token mirrors the shape saved by the original node implementation
// (token.data from the oauth getToken response, plus the region from
// the oauth redirect).
type Token struct {
	AccessToken   string `json:"accessToken"`
	AtExpiredTime int64  `json:"atExpiredTime"`
	RefreshToken  string `json:"refreshToken"`
	RtExpiredTime int64  `json:"rtExpiredTime"`
	Region        string `json:"region"`
}

func (t *Token) Valid() bool {
	return t != nil && t.AccessToken != "" && time.Now().UnixMilli() < t.AtExpiredTime
}

func (t *Token) RefreshValid() bool {
	return t != nil && t.RefreshToken != "" && time.Now().UnixMilli() < t.RtExpiredTime
}

// Store holds the current token, persists it to disk, and lets
// goroutines wait for updates.
type Store struct {
	path string

	mu      sync.Mutex
	tok     *Token
	updated chan struct{}
}

func NewStore(path string) (*Store, error) {
	s := &Store{path: path, updated: make(chan struct{})}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var tok Token
	if err := json.Unmarshal(data, &tok); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	s.tok = &tok
	return s, nil
}

// Get returns a copy of the current token, or nil if there is none.
func (s *Store) Get() *Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tok == nil {
		return nil
	}
	tok := *s.tok
	return &tok
}

// Set persists the token to disk and wakes anyone blocked on Updated().
func (s *Store) Set(tok Token) error {
	data, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tok = &tok
	close(s.updated)
	s.updated = make(chan struct{})
	return nil
}

// Updated returns a channel that is closed the next time the token changes.
func (s *Store) Updated() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updated
}
