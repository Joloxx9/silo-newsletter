// Package newsletter assembles one recipient's weekly newsletter from Silo.
package newsletter

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/images"
	"github.com/joloxx9/silo-newsletter/internal/silo"
)

// Image is one inline picture, referenced from HTML as cid:<CID>.
type Image struct {
	CID  string
	Type string // MIME type
	Data []byte
}

type Title struct {
	ID         string
	Name       string
	Year       int
	Detail     string // e.g. "3 nowe odcinki" or "Dramat · 2 godz. 10 min"
	Overview   string
	IMDb       string // "7.8" or ""
	RTCritic   int    // 0 when unknown
	RTAudience int
	Poster     *Image
	added      time.Time
}

type Popular struct {
	Name    string
	Viewers int
}

type Personal struct {
	Plays     int
	Seconds   float64
	Movies    int
	Episodes  int
	TopShow   string
	TopShowEp int
	Recent    []string
}

type Newsletter struct {
	ServerName    string
	ButtonURL     string
	RecipientID   string
	RecipientName string
	From, To      time.Time // local period, To exclusive
	NewMovies     []Title
	NewShows      []Title
	Popular       []Popular
	Personal      Personal
	Layout        config.Layout
	Logo          *Image
}

func (n Newsletter) Images() []Image {
	var result []Image
	if n.Logo != nil {
		result = append(result, *n.Logo)
	}
	for _, group := range [][]Title{n.NewMovies, n.NewShows} {
		for _, title := range group {
			if title.Poster != nil {
				result = append(result, *title.Poster)
			}
		}
	}
	return result
}

// Builder holds what every recipient of one run shares: the new catalog
// items, server-wide popularity, and downloaded artwork.
type Builder struct {
	client *silo.Client
	cfg    config.Config
	from   time.Time
	to     time.Time
	log    *slog.Logger

	movies    []entry
	shows     []entry
	popular   []Popular
	allPlays  []silo.HistoryEntry
	childByID map[string]map[string]bool
	logo      *Image
}

type entry struct {
	title     Title
	libraries []string
}

// Period returns the [from, to) window ending at the start of today's
// local day plus one day, i.e. the last DaysBack days including today.
func Period(now time.Time, cfg config.Config) (time.Time, time.Time) {
	local := now.In(cfg.Location())
	to := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).AddDate(0, 0, 1)
	return to.AddDate(0, 0, -cfg.DaysBack), to
}

func NewBuilder(ctx context.Context, client *silo.Client, cfg config.Config, now time.Time, log *slog.Logger) (*Builder, error) {
	from, to := Period(now, cfg)
	b := &Builder{client: client, cfg: cfg, from: from, to: to, log: log, childByID: map[string]map[string]bool{}}
	if err := b.loadNewContent(ctx); err != nil {
		return nil, err
	}
	plays, err := client.History(ctx, from, to, "")
	if err != nil {
		return nil, fmt.Errorf("playback history: %w", err)
	}
	b.allPlays = plays
	b.popular = b.computePopular(ctx, plays)
	if cfg.Layout.LogoURL != "" {
		b.logo = b.loadLogo(ctx, cfg.Layout.LogoURL)
	}
	return b, nil
}

func (b *Builder) libraryAllowed(ids []string) bool {
	if len(b.cfg.IncludedLibraryIDs) == 0 {
		return true
	}
	for _, id := range ids {
		for _, included := range b.cfg.IncludedLibraryIDs {
			if id == strings.TrimSpace(included) {
				return true
			}
		}
	}
	return false
}

func (b *Builder) loadNewContent(ctx context.Context) error {
	items, err := b.client.AddedSince(ctx, b.from, "")
	if err != nil {
		return fmt.Errorf("new catalog items: %w", err)
	}
	var movies []silo.CatalogItem
	type episodes struct {
		seriesID, seriesTitle string
		count                 int
		latest                time.Time
		seasons               map[int]bool
		libraries             map[string]bool
	}
	series := map[string]*episodes{}
	for _, item := range items {
		added := item.Added()
		if added.IsZero() || !added.Before(b.to) || !b.libraryAllowed(item.LibraryIDs) {
			continue
		}
		switch item.Type {
		case "movie":
			movies = append(movies, item)
		case "episode", "series":
			id := item.SeriesID
			if item.Type == "series" {
				id = item.ContentID
			}
			if id == "" {
				continue
			}
			group := series[id]
			if group == nil {
				group = &episodes{seriesID: id, seasons: map[int]bool{}, libraries: map[string]bool{}}
				series[id] = group
			}
			if item.Type == "episode" {
				group.count++
				group.seasons[item.SeasonNumber] = true
				if group.seriesTitle == "" {
					group.seriesTitle = item.SeriesTitle
				}
			} else if group.seriesTitle == "" {
				group.seriesTitle = item.Title
			}
			for _, library := range item.LibraryIDs {
				group.libraries[library] = true
			}
			if added.After(group.latest) {
				group.latest = added
			}
		}
	}
	sort.Slice(movies, func(i, j int) bool { return movies[i].Added().After(movies[j].Added()) })
	if len(movies) > b.cfg.MaxMovies {
		movies = movies[:b.cfg.MaxMovies]
	}
	for _, item := range movies {
		title := b.describe(ctx, item.ContentID, item.Title, item.Year)
		title.added = item.Added()
		b.movies = append(b.movies, entry{title: title, libraries: item.LibraryIDs})
	}

	groups := make([]*episodes, 0, len(series))
	for _, group := range series {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].latest.After(groups[j].latest) })
	if len(groups) > b.cfg.MaxShows {
		groups = groups[:b.cfg.MaxShows]
	}
	for _, group := range groups {
		title := b.describe(ctx, group.seriesID, group.seriesTitle, 0)
		if group.count > 0 {
			title.Detail = episodeSummary(group.count, group.seasons)
		} else {
			title.Detail = "Nowy serial"
		}
		title.added = group.latest
		libraries := make([]string, 0, len(group.libraries))
		for library := range group.libraries {
			libraries = append(libraries, library)
		}
		b.shows = append(b.shows, entry{title: title, libraries: libraries})
	}
	return nil
}

func episodeSummary(count int, seasons map[int]bool) string {
	text := fmt.Sprintf("%d %s", count, Plural(count, "nowy odcinek", "nowe odcinki", "nowych odcinków"))
	if len(seasons) == 1 {
		for season := range seasons {
			if season > 0 {
				text += fmt.Sprintf(" · sezon %d", season)
			}
		}
	}
	return text
}

// describe loads ratings, genres, and the poster for one item. A detail
// failure keeps the listing title so the newsletter still renders.
func (b *Builder) describe(ctx context.Context, contentID, fallbackTitle string, fallbackYear int) Title {
	title := Title{ID: contentID, Name: fallbackTitle, Year: fallbackYear}
	detail, err := b.client.Item(ctx, contentID)
	if err != nil {
		b.log.Warn("item detail unavailable", "item", contentID, "error", err)
		return title
	}
	if detail.Title != "" {
		title.Name = detail.Title
	}
	if detail.Year > 0 {
		title.Year = detail.Year
	}
	var parts []string
	if len(detail.Genres) > 0 {
		parts = append(parts, strings.Join(detail.Genres[:min(2, len(detail.Genres))], ", "))
	}
	if detail.Runtime > 0 && detail.Type == "movie" {
		parts = append(parts, FormatMinutes(detail.Runtime))
	}
	title.Detail = strings.Join(parts, " · ")
	title.Overview = shorten(detail.Overview, 220)
	if detail.RatingIMDb > 0 {
		title.IMDb = strconv.FormatFloat(detail.RatingIMDb, 'f', 1, 64)
	}
	title.RTCritic = detail.RatingRTCritic
	title.RTAudience = detail.RatingRTAudience
	poster := detail.PosterURL
	if poster == "" {
		poster = detail.SeriesPosterURL
	}
	if poster != "" {
		if data, err := b.client.Image(ctx, poster); err != nil {
			b.log.Warn("poster download failed", "item", contentID, "error", err)
		} else if jpeg, err := images.ToJPEG(data, 300); err != nil {
			b.log.Warn("poster conversion failed", "item", contentID, "error", err)
		} else {
			title.Poster = &Image{CID: "poster-" + sanitizeCID(contentID) + "@silo-newsletter", Type: "image/jpeg", Data: jpeg}
		}
	}
	return title
}

func (b *Builder) computePopular(ctx context.Context, plays []silo.HistoryEntry) []Popular {
	viewers := map[string]map[string]bool{}
	names := map[string]string{}
	for _, play := range plays {
		if play.Seconds() < 300 && !play.Completed {
			continue
		}
		key, name := play.MediaItemID, play.MediaTitle
		if play.MediaType == "episode" && play.SeriesTitle != "" {
			key, name = "series:"+play.SeriesTitle, play.SeriesTitle
		}
		if viewers[key] == nil {
			viewers[key] = map[string]bool{}
		}
		viewers[key][play.UserID] = true
		names[key] = name
	}
	var result []Popular
	for key, users := range viewers {
		// A title one person watched would reveal that person's viewing on
		// a small server; only titles at least two accounts watched appear.
		if len(users) >= 2 {
			result = append(result, Popular{Name: names[key], Viewers: len(users)})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Viewers != result[j].Viewers {
			return result[i].Viewers > result[j].Viewers
		}
		return result[i].Name < result[j].Name
	})
	return result[:min(5, len(result))]
}

func (b *Builder) childProfiles(ctx context.Context, accountID string) map[string]bool {
	if known, ok := b.childByID[accountID]; ok {
		return known
	}
	children := map[string]bool{}
	if !b.cfg.IncludeChildProfiles {
		profiles, err := b.client.AccountProfiles(ctx, accountID)
		if err != nil {
			b.log.Warn("profile list unavailable; child profiles are counted", "account", accountID, "error", err)
		}
		for _, profile := range profiles {
			if profile.IsChild {
				children[profile.ID] = true
			}
		}
	}
	b.childByID[accountID] = children
	return children
}

// Build returns the newsletter for one account.
func (b *Builder) Build(ctx context.Context, account silo.Account) Newsletter {
	n := Newsletter{
		ServerName:    b.cfg.ServerName,
		ButtonURL:     b.cfg.Button(),
		RecipientID:   account.ID,
		RecipientName: account.Username,
		From:          b.from,
		To:            b.to,
		Popular:       b.popular,
		Layout:        b.cfg.Layout,
		Logo:          b.logo,
	}
	for _, movie := range b.movies {
		if accountCanSee(account.LibraryIDs, movie.libraries) {
			n.NewMovies = append(n.NewMovies, movie.title)
		}
	}
	for _, show := range b.shows {
		if accountCanSee(account.LibraryIDs, show.libraries) {
			n.NewShows = append(n.NewShows, show.title)
		}
	}
	children := b.childProfiles(ctx, account.ID)
	var mine []silo.HistoryEntry
	for _, play := range b.allPlays {
		if play.UserID == account.ID && !children[play.ProfileID] {
			mine = append(mine, play)
		}
	}
	n.Personal = summarize(mine)
	return n
}

func accountCanSee(accountLibraries, itemLibraries []string) bool {
	if len(accountLibraries) == 0 || len(itemLibraries) == 0 {
		return true
	}
	for _, mine := range accountLibraries {
		for _, item := range itemLibraries {
			if mine == item {
				return true
			}
		}
	}
	return false
}

func summarize(plays []silo.HistoryEntry) Personal {
	var personal Personal
	movies := map[string]bool{}
	episodes := map[string]bool{}
	showEpisodes := map[string]map[string]bool{}
	sort.Slice(plays, func(i, j int) bool { return plays[i].EndedAt > plays[j].EndedAt })
	seenRecent := map[string]bool{}
	for _, play := range plays {
		seconds := play.Seconds()
		if seconds < 60 && !play.Completed {
			continue
		}
		personal.Plays++
		personal.Seconds += seconds
		name := play.MediaTitle
		switch play.MediaType {
		case "movie":
			movies[play.MediaItemID] = true
		case "episode":
			episodes[play.MediaItemID] = true
			if play.SeriesTitle != "" {
				if showEpisodes[play.SeriesTitle] == nil {
					showEpisodes[play.SeriesTitle] = map[string]bool{}
				}
				showEpisodes[play.SeriesTitle][play.MediaItemID] = true
				name = play.SeriesTitle
			}
		}
		if !seenRecent[name] && len(personal.Recent) < 5 {
			seenRecent[name] = true
			personal.Recent = append(personal.Recent, name)
		}
	}
	personal.Movies = len(movies)
	personal.Episodes = len(episodes)
	for show, watched := range showEpisodes {
		if len(watched) > personal.TopShowEp || len(watched) == personal.TopShowEp && show < personal.TopShow {
			personal.TopShow, personal.TopShowEp = show, len(watched)
		}
	}
	return personal
}

func shorten(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	cut := string(runes[:limit])
	if space := strings.LastIndex(cut, " "); space > limit/2 {
		cut = cut[:space]
	}
	return strings.TrimRight(cut, ",.;: ") + "…"
}

func sanitizeCID(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, value)
}

const maxLogoBytes = 5 << 20

// loadLogo downloads the header logo once per run. A broken logo only
// leaves the header without it.
func (b *Builder) loadLogo(ctx context.Context, address string) *Image {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		b.log.Warn("logo address is invalid", "error", err)
		return nil
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		b.log.Warn("logo download failed", "error", err)
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		b.log.Warn("logo download failed", "status", response.Status)
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxLogoBytes+1))
	if err != nil || len(data) > maxLogoBytes {
		b.log.Warn("logo download failed", "error", err, "bytes", len(data))
		return nil
	}
	encoded, err := images.ToPNG(data, 480)
	if err != nil {
		b.log.Warn("logo conversion failed", "error", err)
		return nil
	}
	return &Image{CID: "logo@silo-newsletter", Type: "image/png", Data: encoded}
}
