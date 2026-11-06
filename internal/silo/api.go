package silo

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

type Account struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Enabled  *bool  `json:"enabled"`
	IsOwner  bool   `json:"is_owner"`
	// LibraryIDs restricts the account's access; empty means every library.
	LibraryIDs []string `json:"library_ids"`
}

func (a Account) Active() bool { return a.Enabled == nil || *a.Enabled }

type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsChild   bool   `json:"is_child"`
	IsPrimary bool   `json:"is_primary"`
}

type Library struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type HistoryEntry struct {
	UserID               string   `json:"user_id"`
	ProfileID            string   `json:"profile_id"`
	MediaItemID          string   `json:"media_item_id"`
	MediaTitle           string   `json:"media_title"`
	MediaType            string   `json:"media_type"`
	SeriesTitle          string   `json:"series_title"`
	SeasonNumber         *int     `json:"season_number"`
	EpisodeNumber        *int     `json:"episode_number"`
	StartedAt            string   `json:"started_at"`
	EndedAt              string   `json:"ended_at"`
	Completed            bool     `json:"completed"`
	Measured             bool     `json:"measured"`
	PlayedSeconds        *float64 `json:"played_seconds"`
	WatchedSeconds       float64  `json:"watched_seconds"`
	DurationSeconds      *float64 `json:"duration_seconds"`
	StartPositionSeconds *float64 `json:"start_position_seconds"`
	DevicePlatform       *string  `json:"device_platform"`
	ClientName           *string  `json:"client_name"`
}

// Seconds is the best estimate of how long this attempt actually played.
func (h HistoryEntry) Seconds() float64 {
	if h.PlayedSeconds != nil && *h.PlayedSeconds > 0 {
		return *h.PlayedSeconds
	}
	return h.WatchedSeconds
}

type CatalogItem struct {
	ContentID     string   `json:"content_id"`
	Type          string   `json:"type"`
	Title         string   `json:"title"`
	Year          int      `json:"year"`
	AddedAt       string   `json:"added_at"`
	SeriesID      string   `json:"series_id"`
	SeriesTitle   string   `json:"series_title"`
	SeasonNumber  int      `json:"season_number"`
	EpisodeNumber int      `json:"episode_number"`
	LibraryIDs    []string `json:"library_ids"`
	Status        string   `json:"status"`
}

func (i CatalogItem) Added() time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, i.AddedAt)
	return parsed
}

type ItemDetail struct {
	ContentID        string   `json:"content_id"`
	Type             string   `json:"type"`
	Title            string   `json:"title"`
	Year             int      `json:"year"`
	Overview         string   `json:"overview"`
	Tagline          string   `json:"tagline"`
	Genres           []string `json:"genres"`
	Runtime          int      `json:"runtime"`
	ContentRating    string   `json:"content_rating"`
	SeriesID         string   `json:"series_id"`
	SeriesTitle      string   `json:"series_title"`
	SeasonCount      int      `json:"season_count"`
	PosterURL        string   `json:"poster_url"`
	BackdropURL      string   `json:"backdrop_url"`
	LogoURL          string   `json:"logo_url"`
	SeriesPosterURL  string   `json:"series_poster_url"`
	SeasonPosterURL  string   `json:"season_poster_url"`
	RatingIMDb       float64  `json:"rating_imdb"`
	RatingTMDb       float64  `json:"rating_tmdb"`
	RatingRTCritic   int      `json:"rating_rt_critic"`
	RatingRTAudience int      `json:"rating_rt_audience"`
}

func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	return paged[Account](ctx, c, "/api/v2/admin/users", url.Values{"limit": {"200"}}, "")
}

// AccountProfiles lists an account's profiles. Silo may omit is_child here;
// a missing flag is treated as an adult profile.
func (c *Client) AccountProfiles(ctx context.Context, accountID string) ([]Profile, error) {
	return paged[Profile](ctx, c, "/api/v2/admin/users/"+url.PathEscape(accountID)+"/profiles", nil, "")
}

// OwnProfile returns the key owner's primary (or first adult) profile, used
// for catalog detail reads.
func (c *Client) OwnProfile(ctx context.Context) (string, error) {
	if c.profileID != "" {
		return c.profileID, nil
	}
	var document page[Profile]
	if err := c.get(ctx, "/api/v2/profiles", nil, "", &document); err != nil {
		return "", err
	}
	chosen := ""
	for _, profile := range document.Items {
		if profile.IsPrimary {
			chosen = profile.ID
			break
		}
		if !profile.IsChild && chosen == "" {
			chosen = profile.ID
		}
	}
	if chosen == "" {
		return "", errors.New("konto klucza API nie ma profilu; użyj klucza administratora bez ograniczeń uprawnień")
	}
	c.profileID = chosen
	return chosen, nil
}

func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	profileID, err := c.OwnProfile(ctx)
	if err != nil {
		return nil, err
	}
	var document page[Library]
	if err := c.get(ctx, "/api/v2/user/libraries", nil, profileID, &document); err != nil {
		return nil, err
	}
	return document.Items, nil
}

// History lists finalized plays that ended in [from, to). An empty userID
// lists every account.
func (c *Client) History(ctx context.Context, from, to time.Time, userID string) ([]HistoryEntry, error) {
	query := url.Values{
		"limit":        {"200"},
		"ended_after":  {Instant(from)},
		"ended_before": {Instant(to)},
	}
	if userID != "" {
		query.Set("user_id", userID)
	}
	entries, err := paged[HistoryEntry](ctx, c, "/api/v2/admin/playback-history", query, "")
	if err != nil {
		return nil, err
	}
	if userID != "" {
		// Defense in depth: never let another account's row through.
		kept := entries[:0]
		for _, entry := range entries {
			if entry.UserID == userID {
				kept = append(kept, entry)
			}
		}
		entries = kept
	}
	return entries, nil
}

// AddedSince lists catalog items Silo first saw at or after since.
func (c *Client) AddedSince(ctx context.Context, since time.Time, itemType string) ([]CatalogItem, error) {
	query := url.Values{"limit": {"200"}, "added_after": {Instant(since)}}
	if itemType != "" {
		query.Set("type", itemType)
	}
	return paged[CatalogItem](ctx, c, "/api/v2/admin/catalog/items", query, "")
}

// CatalogItems resolves listing rows for content identities.
func (c *Client) CatalogItems(ctx context.Context, ids []string) (map[string]CatalogItem, error) {
	result := make(map[string]CatalogItem, len(ids))
	for start := 0; start < len(ids); start += 200 {
		end := min(start+200, len(ids))
		query := url.Values{"limit": {"200"}, "content_ids": ids[start:end]}
		items, err := paged[CatalogItem](ctx, c, "/api/v2/admin/catalog/items", query, "")
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			result[item.ContentID] = item
		}
	}
	return result, nil
}

// Item returns one item's detail: ratings, genres, and artwork addresses.
func (c *Client) Item(ctx context.Context, contentID string) (ItemDetail, error) {
	profileID, err := c.OwnProfile(ctx)
	if err != nil {
		return ItemDetail{}, err
	}
	var detail ItemDetail
	err = c.get(ctx, "/api/v2/catalog/items/"+url.PathEscape(contentID), url.Values{"image_size": {"large"}}, profileID, &detail)
	return detail, err
}

// Image downloads artwork. Silo artwork addresses are signed, so no API key
// is sent with them. Only server-relative addresses or addresses on the
// configured Silo host are fetched.
func (c *Client) Image(ctx context.Context, address string) ([]byte, error) {
	resolved, err := c.resolveImage(address)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, resolved, "artwork", "", false, maxImageBytes)
}

func (c *Client) resolveImage(address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("no artwork address")
	}
	if strings.HasPrefix(address, "/") && !strings.HasPrefix(address, "//") {
		reference, err := url.Parse(address)
		if err != nil {
			return "", err
		}
		return c.base.ResolveReference(reference).String(), nil
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host != c.base.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("artwork address is not on the Silo server")
	}
	return parsed.String(), nil
}

// Check proves the key can do every read a newsletter needs.
func (c *Client) Check(ctx context.Context) (accounts, libraries int, err error) {
	accountList, err := c.Accounts(ctx)
	if err != nil {
		return 0, 0, err
	}
	now := time.Now()
	if _, err := c.History(ctx, now.Add(-24*time.Hour), now, ""); err != nil {
		return 0, 0, err
	}
	libraryList, err := c.Libraries(ctx)
	if err != nil {
		return 0, 0, err
	}
	var sample page[CatalogItem]
	err = c.get(ctx, "/api/v2/admin/catalog/items", url.Values{"limit": {"1"}}, "", &sample)
	if err == nil && len(sample.Items) > 0 {
		_, err = c.Item(ctx, sample.Items[0].ContentID)
	}
	if err != nil {
		return 0, 0, err
	}
	return len(accountList), len(libraryList), nil
}
