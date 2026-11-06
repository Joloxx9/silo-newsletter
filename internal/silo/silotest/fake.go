// Package silotest provides a deterministic fake Silo /api/v2 server with
// fictional accounts and titles for tests.
package silotest

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	Key       = "sa_fictional_admin_key"
	ProfileID = "c0ffee00-0000-4000-8000-000000000001"
	KidProfle = "kid-profile-1"
	// PageSize is small so paging is always exercised.
	PageSize = 2
)

//go:embed testdata/poster.webp
var poster []byte

type Request struct {
	Method        string
	Path          string
	Query         string
	Authorization string
	ProfileID     string
}

type Fake struct {
	Server *httptest.Server
	// Now anchors the fictional catalog and history.
	Now time.Time

	mu          sync.Mutex
	requests    []Request
	rateLimited bool
}

func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

func (f *Fake) URL() string { return f.Server.URL }

func New(t *testing.T, now time.Time) *Fake {
	t.Helper()
	f := &Fake{Now: now.UTC()}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

func (f *Fake) ago(days float64) string {
	return f.Now.Add(-time.Duration(days * 24 * float64(time.Hour))).Format(time.RFC3339)
}

func (f *Fake) accounts() []map[string]any {
	yes, no := true, false
	return []map[string]any{
		{"id": "1", "username": "alice", "email": "alice@example.com", "role": "admin", "enabled": yes, "is_owner": true},
		{"id": "2", "username": "bob", "email": "bob@example.com", "role": "user", "enabled": yes, "library_ids": []string{"10"}},
		{"id": "3", "username": "carol", "email": "carol@example.com", "role": "user", "enabled": no},
		{"id": "4", "username": "dave", "email": "", "role": "user", "enabled": yes},
	}
}

func (f *Fake) catalog() []map[string]any {
	return []map[string]any{
		{"content_id": "movie-tmdb-1001", "type": "movie", "title": "Fikcyjny Film", "year": 2026, "added_at": f.ago(1), "library_ids": []string{"10"}},
		{"content_id": "movie-tmdb-1002", "type": "movie", "title": "Zgniły Pomidor", "year": 2025, "added_at": f.ago(3), "library_ids": []string{"10"}},
		{"content_id": "movie-tmdb-1003", "type": "movie", "title": "Stary Klasyk", "year": 1999, "added_at": f.ago(40), "library_ids": []string{"10"}},
		{"content_id": "episode-tvdb-2001-1-1", "type": "episode", "title": "Pilot", "series_id": "series-tvdb-2001", "series_title": "Serial Testowy", "season_number": 1, "episode_number": 1, "added_at": f.ago(2), "library_ids": []string{"11"}},
		{"content_id": "episode-tvdb-2001-1-2", "type": "episode", "title": "Drugi", "series_id": "series-tvdb-2001", "series_title": "Serial Testowy", "season_number": 1, "episode_number": 2, "added_at": f.ago(2), "library_ids": []string{"11"}},
	}
}

func (f *Fake) history() []map[string]any {
	played := func(seconds float64) *float64 { return &seconds }
	return []map[string]any{
		{"user_id": "1", "profile_id": "alice-main", "media_item_id": "movie-tmdb-1001", "media_title": "Fikcyjny Film", "media_type": "movie", "ended_at": f.ago(1), "completed": true, "played_seconds": played(6000)},
		{"user_id": "1", "profile_id": "alice-main", "media_item_id": "episode-tvdb-2001-1-1", "media_title": "Pilot", "media_type": "episode", "series_title": "Serial Testowy", "ended_at": f.ago(1.5), "completed": true, "played_seconds": played(2400)},
		{"user_id": "1", "profile_id": KidProfle, "media_item_id": "movie-tmdb-9999", "media_title": "Bajka Dla Dzieci", "media_type": "movie", "ended_at": f.ago(2), "completed": true, "played_seconds": played(4000)},
		{"user_id": "2", "profile_id": "bob-main", "media_item_id": "movie-tmdb-1001", "media_title": "Fikcyjny Film", "media_type": "movie", "ended_at": f.ago(2), "completed": true, "played_seconds": played(6100)},
		{"user_id": "2", "profile_id": "bob-main", "media_item_id": "movie-tmdb-1003", "media_title": "Prywatny Seans Boba", "media_type": "movie", "ended_at": f.ago(2.5), "completed": false, "played_seconds": played(900)},
	}
}

func (f *Fake) details() map[string]map[string]any {
	return map[string]map[string]any{
		"movie-tmdb-1001":  {"content_id": "movie-tmdb-1001", "type": "movie", "title": "Fikcyjny Film", "year": 2026, "overview": "Bohater wyrusza w podróż.", "genres": []string{"Przygodowy", "Dramat"}, "runtime": 130, "rating_imdb": 7.8, "rating_rt_critic": 91, "rating_rt_audience": 85, "poster_url": "/artwork/movie-1001.webp?sig=fictional"},
		"movie-tmdb-1002":  {"content_id": "movie-tmdb-1002", "type": "movie", "title": "Zgniły Pomidor", "year": 2025, "rating_imdb": 4.1, "rating_rt_critic": 22, "rating_rt_audience": 35, "poster_url": "/artwork/movie-1002.webp?sig=fictional"},
		"series-tvdb-2001": {"content_id": "series-tvdb-2001", "type": "series", "title": "Serial Testowy", "year": 2024, "rating_imdb": 8.4, "poster_url": "https://elsewhere.example.net/steal.webp"},
	}
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"), ProfileID: r.Header.Get("X-Profile-Id")})
	limitOnce := !f.rateLimited && strings.HasPrefix(r.URL.Path, "/api/v2/catalog/items/")
	if limitOnce {
		f.rateLimited = true
	}
	f.mu.Unlock()

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/artwork/") {
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("sig") != "fictional" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		_, _ = w.Write(poster)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+Key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if limitOnce {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	// Like Silo, admin routes require the caller's primary profile.
	if strings.HasPrefix(r.URL.Path, "/api/v2/admin/") && r.Header.Get("X-Profile-Id") != ProfileID {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	query := r.URL.Query()
	switch {
	case r.URL.Path == "/api/v2/admin/users":
		f.page(w, r, f.accounts())
	case r.URL.Path == "/api/v2/admin/users/1/profiles":
		f.page(w, r, []map[string]any{{"id": "alice-main", "name": "Alice"}, {"id": KidProfle, "name": "Dziecko", "is_child": true}})
	case strings.HasPrefix(r.URL.Path, "/api/v2/admin/users/") && strings.HasSuffix(r.URL.Path, "/profiles"):
		f.page(w, r, []map[string]any{})
	case r.URL.Path == "/api/v2/profiles":
		writeJSON(w, map[string]any{"items": []map[string]any{{"id": KidProfle, "is_child": true}, {"id": ProfileID, "is_primary": true}}})
	case r.URL.Path == "/api/v2/user/libraries":
		if r.Header.Get("X-Profile-Id") != ProfileID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"items": []map[string]any{{"id": "10", "name": "Filmy", "type": "movies"}, {"id": "11", "name": "Seriale", "type": "series"}}})
	case r.URL.Path == "/api/v2/admin/playback-history":
		after, _ := time.Parse(time.RFC3339Nano, query.Get("ended_after"))
		before, _ := time.Parse(time.RFC3339Nano, query.Get("ended_before"))
		var rows []map[string]any
		for _, row := range f.history() {
			ended, _ := time.Parse(time.RFC3339, row["ended_at"].(string))
			if ended.Before(after) || !ended.Before(before) {
				continue
			}
			if user := query.Get("user_id"); user != "" && row["user_id"] != user {
				continue
			}
			rows = append(rows, row)
		}
		f.page(w, r, rows)
	case r.URL.Path == "/api/v2/admin/catalog/items":
		after, _ := time.Parse(time.RFC3339Nano, query.Get("added_after"))
		ids := map[string]bool{}
		for _, id := range query["content_ids"] {
			ids[id] = true
		}
		var rows []map[string]any
		for _, row := range f.catalog() {
			added, _ := time.Parse(time.RFC3339, row["added_at"].(string))
			if added.Before(after) || len(ids) > 0 && !ids[row["content_id"].(string)] {
				continue
			}
			if kind := query.Get("type"); kind != "" && row["type"] != kind {
				continue
			}
			rows = append(rows, row)
		}
		f.page(w, r, rows)
	case strings.HasPrefix(r.URL.Path, "/api/v2/catalog/items/"):
		if r.Header.Get("X-Profile-Id") != ProfileID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		detail, ok := f.details()[strings.TrimPrefix(r.URL.Path, "/api/v2/catalog/items/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, detail)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// page serves rows PageSize at a time with opaque numeric cursors.
func (f *Fake) page(w http.ResponseWriter, r *http.Request, rows []map[string]any) {
	start := 0
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		for _, character := range cursor {
			start = start*10 + int(character-'0')
		}
	}
	end := min(start+PageSize, len(rows))
	if start > len(rows) {
		start = len(rows)
	}
	pageInfo := map[string]any{"has_more": end < len(rows)}
	if end < len(rows) {
		pageInfo["next_cursor"] = itoa(end)
	}
	writeJSON(w, map[string]any{"items": rows[start:end], "page": pageInfo})
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
