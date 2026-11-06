// Package render turns a newsletter into email HTML and plain text.
package render

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	textTemplate "text/template"

	"github.com/joloxx9/silo-newsletter/internal/newsletter"
)

//go:embed templates/*
var files embed.FS

var funcs = map[string]any{
	"period":   func(n newsletter.Newsletter) string { return newsletter.FormatPeriod(n.From, n.To) },
	"duration": newsletter.FormatDuration,
	"plural":   newsletter.Plural,
	"upper":    strings.ToUpper,
	"rtFresh":  func(score int) bool { return score >= 60 },
	"add":      func(a, b int) int { return a + b },
	"titlesView": func(v view, items []newsletter.Title) titlesData {
		return titlesData{Items: items, ImageSrc: v.ImageSrc}
	},
}

type titlesData struct {
	Items    []newsletter.Title
	ImageSrc func(*newsletter.Image) template.URL
}

var (
	htmlTemplates = template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/email.html"))
	textTemplates = textTemplate.Must(textTemplate.New("").Funcs(funcs).ParseFS(files, "templates/email.txt"))
)

type view struct {
	newsletter.Newsletter
	ImageSrc func(*newsletter.Image) template.URL
}

// Subject returns the email subject line.
func Subject(n newsletter.Newsletter) string {
	count := len(n.NewMovies) + len(n.NewShows)
	if count == 0 {
		return fmt.Sprintf("Twój tydzień na %s", n.ServerName)
	}
	return fmt.Sprintf("Nowości na %s: %d %s w tym tygodniu", n.ServerName, count, newsletter.Plural(count, "tytuł", "tytuły", "tytułów"))
}

// HTML renders the email body. Images point at cid: references for email;
// with inline set they are embedded as data: URLs for a browser preview.
func HTML(n newsletter.Newsletter, inline bool) (string, error) {
	v := view{Newsletter: n, ImageSrc: func(image *newsletter.Image) template.URL {
		if inline {
			return template.URL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(image.Data))
		}
		return template.URL("cid:" + image.CID)
	}}
	var output bytes.Buffer
	if err := htmlTemplates.ExecuteTemplate(&output, "email.html", v); err != nil {
		return "", err
	}
	return output.String(), nil
}

func Text(n newsletter.Newsletter) (string, error) {
	var output bytes.Buffer
	if err := textTemplates.ExecuteTemplate(&output, "email.txt", n); err != nil {
		return "", err
	}
	return output.String(), nil
}
