// Package silo is a read-only client for the Silo media server /api/v2.
package silo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 8 << 20
	maxImageBytes    = 12 << 20
	maxPages         = 200
	maxRetries       = 5
)

// Client talks to one Silo server with one administrator API key.
type Client struct {
	base *url.URL
	key  string
	http *http.Client
	// Sleep waits between retries; tests replace it.
	Sleep func(context.Context, time.Duration) error
	// profileID is the key owner's profile used for catalog detail reads.
	profileID string
}

// APIError is a non-success HTTP answer. It never contains the key or URL.
type APIError struct {
	Path   string
	Status int
}

func (e *APIError) Error() string {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("Silo odrzuciło klucz API (HTTP %d, %s); użyj klucza administratora bez ograniczeń uprawnień", e.Status, e.Path)
	default:
		return fmt.Sprintf("Silo odpowiedziało HTTP %d dla %s", e.Status, e.Path)
	}
}

func New(baseURL, key string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("invalid Silo URL")
	}
	return &Client{
		base: parsed,
		key:  key,
		http: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are not followed")
			},
		},
		Sleep: sleepContext,
	}, nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) endpoint(path string, query url.Values) string {
	endpoint := *c.base
	endpoint.Path = strings.TrimRight(c.base.Path, "/") + path
	endpoint.RawQuery = query.Encode()
	return endpoint.String()
}

// get performs one authenticated GET, retrying 429 and 503 answers.
func (c *Client) get(ctx context.Context, path string, query url.Values, profileID string, target any) error {
	// Silo requires the key owner's primary profile on admin routes, so
	// every authenticated call except the profile lookup itself carries it.
	if profileID == "" && path != "/api/v2/profiles" {
		own, err := c.OwnProfile(ctx)
		if err != nil {
			return err
		}
		profileID = own
	}
	body, err := c.do(ctx, c.endpoint(path, query), path, profileID, true, maxResponseBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("Silo returned unreadable JSON for %s", path)
	}
	return nil
}

func (c *Client) do(ctx context.Context, address, label, profileID string, authenticated bool, limit int64) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		if authenticated {
			request.Header.Set("Authorization", "Bearer "+c.key)
			request.Header.Set("Accept", "application/json")
		}
		if profileID != "" {
			request.Header.Set("X-Profile-Id", profileID)
		}
		response, err := c.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("brak połączenia z Silo (%s); sprawdź adres serwera", label)
		}
		if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusServiceUnavailable) && attempt < maxRetries {
			delay := retryDelay(response.Header.Get("Retry-After"), attempt)
			response.Body.Close()
			if err := c.Sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, &APIError{Path: label, Status: response.StatusCode}
		}
		if readErr != nil || int64(len(body)) > limit {
			return nil, fmt.Errorf("Silo response for %s was unreadable or too large", label)
		}
		return body, nil
	}
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && seconds >= 0 {
		return time.Duration(min(seconds, 60)) * time.Second
	}
	if when, err := http.ParseTime(header); err == nil {
		if delay := time.Until(when); delay > 0 {
			return min(delay, 60*time.Second)
		}
		return 0
	}
	return time.Duration(1<<attempt) * time.Second
}

type page[T any] struct {
	Items []T `json:"items"`
	Page  struct {
		HasMore    bool   `json:"has_more"`
		NextCursor string `json:"next_cursor"`
	} `json:"page"`
}

func paged[T any](ctx context.Context, c *Client, path string, query url.Values, profileID string) ([]T, error) {
	var all []T
	cursor := ""
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		pageQuery := url.Values{}
		for name, values := range query {
			pageQuery[name] = values
		}
		if cursor != "" {
			pageQuery.Set("cursor", cursor)
		}
		var document page[T]
		if err := c.get(ctx, path, pageQuery, profileID, &document); err != nil {
			return nil, err
		}
		all = append(all, document.Items...)
		if !document.Page.HasMore || document.Page.NextCursor == "" {
			return all, nil
		}
		cursor = document.Page.NextCursor
	}
	return all, fmt.Errorf("Silo listing %s exceeded %d pages", path, maxPages)
}

// Instant formats a time as the RFC 3339 UTC instant Silo expects.
func Instant(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
