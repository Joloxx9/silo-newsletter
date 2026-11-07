package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// Section IDs, in the default newsletter order.
const (
	SectionPersonal = "personal"
	SectionMovies   = "movies"
	SectionShows    = "shows"
	SectionPopular  = "popular"
)

var sectionIDs = []string{SectionPersonal, SectionMovies, SectionShows, SectionPopular}

// Layout controls how the newsletter looks. Texts may use {imie}, {serwer}
// and {okres}, replaced per recipient.
type Layout struct {
	Sections    []Section `json:"Sections"`    // display order
	Theme       string    `json:"Theme"`       // "dark" or "light"
	AccentColor string    `json:"AccentColor"` // #rrggbb; "" = the theme's own
	Heading     string    `json:"Heading"`
	Greeting    string    `json:"Greeting"`
	Intro       string    `json:"Intro"`
	LogoURL     string    `json:"LogoUrl"`

	ShowYear     bool `json:"ShowYear"`
	ShowDetail   bool `json:"ShowDetail"`
	ShowRatings  bool `json:"ShowRatings"`
	ShowOverview bool `json:"ShowOverview"`
}

type Section struct {
	ID      string `json:"Id"`
	Enabled bool   `json:"Enabled"`
	// Style is "list" or "grid"; it applies to movies and shows only.
	Style   string `json:"Style"`
	Columns int    `json:"Columns"` // grid posters per row, 2-4
	// Width is "full" or "half"; two half sections in a row sit side by side.
	Width string `json:"Width"`
}

func DefaultLayout() Layout {
	layout := Layout{
		Theme:        "dark",
		Heading:      "{serwer} · newsletter",
		Greeting:     "Cześć, {imie}!",
		Intro:        "Podsumowanie tygodnia: {okres}",
		ShowYear:     true,
		ShowDetail:   true,
		ShowRatings:  true,
		ShowOverview: true,
	}
	layout.Normalize()
	return layout
}

// HasStyle reports whether the section offers the list/grid choice.
func (s Section) HasStyle() bool { return s.ID == SectionMovies || s.ID == SectionShows }

// Normalize keeps every known section exactly once (adding missing ones,
// enabled, at the end) and fills empty choices with defaults.
func (l *Layout) Normalize() {
	seen := map[string]bool{}
	var sections []Section
	for _, section := range l.Sections {
		if !known(section.ID) || seen[section.ID] {
			continue
		}
		seen[section.ID] = true
		sections = append(sections, section)
	}
	for _, id := range sectionIDs {
		if !seen[id] {
			sections = append(sections, Section{ID: id, Enabled: true})
		}
	}
	for index := range sections {
		section := &sections[index]
		if section.Style != "grid" {
			section.Style = "list"
		}
		if section.Columns < 2 || section.Columns > 4 {
			section.Columns = 3
		}
		if section.Width != "half" {
			section.Width = "full"
		}
	}
	l.Sections = sections
	l.Theme = strings.ToLower(strings.TrimSpace(l.Theme))
	if l.Theme != "light" {
		l.Theme = "dark"
	}
	l.AccentColor = strings.ToLower(strings.TrimSpace(l.AccentColor))
	l.Heading = strings.TrimSpace(l.Heading)
	l.Greeting = strings.TrimSpace(l.Greeting)
	l.Intro = strings.TrimSpace(l.Intro)
	l.LogoURL = strings.TrimSpace(l.LogoURL)
}

func known(id string) bool {
	for _, candidate := range sectionIDs {
		if candidate == id {
			return true
		}
	}
	return false
}

var hexColor = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Validate checks a normalized layout.
func (l Layout) Validate() error {
	if l.AccentColor != "" && !hexColor.MatchString(l.AccentColor) {
		return errors.New("kolor akcentu musi mieć postać #rrggbb")
	}
	for _, text := range []string{l.Heading, l.Greeting, l.Intro} {
		if len([]rune(text)) > 200 {
			return errors.New("teksty nagłówka mogą mieć najwyżej 200 znaków")
		}
	}
	if l.LogoURL != "" {
		parsed, err := url.Parse(l.LogoURL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("adres logo musi zaczynać się od http:// lub https://")
		}
	}
	return nil
}
