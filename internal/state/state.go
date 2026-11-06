// Package state persists the panel password, delivery history, and the
// schedule slots already sent, in one JSON file next to the config.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const maxHistory = 100

type Run struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // "scheduled", "manual", "test"
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Sent       int       `json:"sent"`
	Failed     int       `json:"failed"`
	Skipped    int       `json:"skipped"`
	Status     string    `json:"status"` // "running", "done", "failed"
	Message    string    `json:"message,omitempty"`
	Failures   []string  `json:"failures,omitempty"` // "account name: reason", no addresses
}

type data struct {
	PasswordHash string `json:"passwordHash,omitempty"`
	// LastSlot is the last scheduled send time claimed (RFC 3339). A slot is
	// claimed before sending so a restart never sends it twice.
	LastSlot string `json:"lastSlot,omitempty"`
	Runs     []Run  `json:"runs,omitempty"`
}

type Store struct {
	path string
	mu   sync.Mutex
	data data
}

func Open(path string) (*Store, error) {
	store := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &store.data); err != nil {
			return nil, errors.New("state file is unreadable; move it aside to reset the panel password and history")
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, err
	}
	// A run left "running" by a crash or restart did not finish.
	for index := range store.data.Runs {
		if store.data.Runs[index].Status == "running" {
			store.data.Runs[index].Status = "failed"
			store.data.Runs[index].Message = "przerwane przez restart aplikacji"
		}
	}
	return store, nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, s.path)
}

func (s *Store) HasPassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.PasswordHash != ""
}

func (s *Store) SetPassword(password string) error {
	if len(password) < 8 {
		return errors.New("hasło musi mieć co najmniej 8 znaków")
	}
	if len(password) > 72 {
		return errors.New("hasło może mieć najwyżej 72 znaki")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.PasswordHash = string(hash)
	return s.saveLocked()
}

func (s *Store) CheckPassword(password string) bool {
	s.mu.Lock()
	hash := s.data.PasswordHash
	s.mu.Unlock()
	return hash != "" && bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// ClaimSlot records slot as sent if it is newer than the last claimed slot.
func (s *Store) ClaimSlot(slot time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if last, err := time.Parse(time.RFC3339, s.data.LastSlot); err == nil && !slot.After(last) {
		return false, nil
	}
	s.data.LastSlot = slot.UTC().Format(time.RFC3339)
	return true, s.saveLocked()
}

func (s *Store) LastSlot() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, _ := time.Parse(time.RFC3339, s.data.LastSlot)
	return last
}

// SaveRun inserts or replaces a run by ID, newest first.
func (s *Store) SaveRun(run Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.data.Runs {
		if s.data.Runs[index].ID == run.ID {
			s.data.Runs[index] = run
			return s.saveLocked()
		}
	}
	s.data.Runs = append([]Run{run}, s.data.Runs...)
	if len(s.data.Runs) > maxHistory {
		s.data.Runs = s.data.Runs[:maxHistory]
	}
	return s.saveLocked()
}

func (s *Store) Runs() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Run(nil), s.data.Runs...)
}
