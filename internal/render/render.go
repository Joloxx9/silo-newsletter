// Package render turns a newsletter into email HTML and plain text.
package render

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strconv"
	"strings"
	textTemplate "text/template"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/images"
	"github.com/joloxx9/silo-newsletter/internal/newsletter"
)

//go:embed templates/*
var files embed.FS

var funcs = map[string]any{
	"duration": newsletter.FormatDuration,
	"plural":   newsletter.Plural,
	"upper":    strings.ToUpper,
	"rtFresh":  func(score int) bool { return score >= 60 },
	"add":      func(a, b int) int { return a + b },
	"part":     func(v view, b Block) blockView { return blockView{Block: b, V: v} },
}

var (
	htmlTemplates = template.Must(template.New("").Funcs(funcs).ParseFS(files, "templates/email.html"))
	textTemplates = textTemplate.Must(textTemplate.New("").Funcs(funcs).ParseFS(files, "templates/email.txt"))
)

// Palette holds the email colors for one theme.
type Palette struct {
	Background, Card, Strong, Text, Muted, Faint, Footer, Accent, ButtonText string
}

var palettes = map[string]Palette{
	"dark":  {Background: "#0d0f14", Card: "#161a23", Strong: "#ffffff", Text: "#d5d9e4", Muted: "#a9afbf", Faint: "#8a90a2", Footer: "#6c7286", Accent: "#8fa3ff"},
	"light": {Background: "#eef0f4", Card: "#ffffff", Strong: "#111318", Text: "#2a2f3a", Muted: "#565c6b", Faint: "#737989", Footer: "#868b99", Accent: "#4253e0"},
}

// Block is one enabled newsletter section, in the configured order.
type Block struct {
	config.Section
	Heading string
	Items   []newsletter.Title
	Rows    [][]newsletter.Title // grid only
	// Quiet shows the "nothing new" note in place of an empty catalog section.
	Quiet bool
	// Half is set when the block shares its row with another one.
	Half bool
}

// Row is one full-width block or two half-width blocks side by side.
type Row struct {
	Blocks []Block
}

type view struct {
	newsletter.Newsletter
	ImageSrc func(*newsletter.Image) template.URL
	C        Palette
	Period   string
	Heading  string
	Greeting string
	Intro    string
	Blocks   []Block
	Rows     []Row
}

type blockView struct {
	Block
	V view
}

// Gap is the space between grid cards in pixels.
func (b blockView) Gap() int { return 8 }

// CellWidth is the grid poster width in pixels for the 640px email: the
// row minus the gaps, split into columns, minus the card padding.
func (b blockView) CellWidth() int {
	row := 600
	if b.Half {
		row = 290
	}
	return (row-b.Gap()*(b.Columns+1))/b.Columns - 16
}

// CellHeight keeps every grid poster at the same 2:3 shape.
func (b blockView) CellHeight() int {
	return b.CellWidth() * images.PosterHeight / images.PosterWidth
}

// CellPercent is the grid column width as an HTML attribute value.
func (b blockView) CellPercent() string { return strconv.Itoa(100/b.Columns) + "%" }

// Fill returns one entry per empty cell that pads a short last grid row.
func (b blockView) Fill(row []newsletter.Title) []struct{} {
	return make([]struct{}, b.Columns-len(row))
}

// Subject returns the email subject line.
func Subject(n newsletter.Newsletter) string {
	count := len(n.NewMovies) + len(n.NewShows)
	if count == 0 {
		return fmt.Sprintf("Twój tydzień na %s", n.ServerName)
	}
	return fmt.Sprintf("Nowości na %s: %d %s w tym tygodniu", n.ServerName, count, newsletter.Plural(count, "tytuł", "tytuły", "tytułów"))
}

func newView(n newsletter.Newsletter) view {
	layout := n.Layout
	if len(layout.Sections) == 0 {
		layout = config.DefaultLayout()
	}
	layout.Normalize()
	n.Layout = layout
	palette := palettes[layout.Theme]
	if layout.AccentColor != "" {
		palette.Accent = layout.AccentColor
	}
	palette.ButtonText = "#ffffff"
	if luminance(palette.Accent) > 0.6 {
		palette.ButtonText = "#111318"
	}
	period := newsletter.FormatPeriod(n.From, n.To)
	fill := strings.NewReplacer("{imie}", n.RecipientName, "{serwer}", n.ServerName, "{okres}", period).Replace
	v := view{Newsletter: n, C: palette, Period: period, Heading: fill(layout.Heading), Greeting: fill(layout.Greeting),
		Intro: fill(layout.Intro), Blocks: blocks(n)}
	v.Rows = rows(v.Blocks)
	return v
}

func blocks(n newsletter.Newsletter) []Block {
	quiet := len(n.NewMovies) == 0 && len(n.NewShows) == 0
	var result []Block
	for _, section := range n.Layout.Sections {
		if !section.Enabled {
			continue
		}
		block := Block{Section: section}
		switch section.ID {
		case config.SectionPersonal:
		case config.SectionMovies, config.SectionShows:
			block.Heading, block.Items = "NOWE FILMY", n.NewMovies
			if section.ID == config.SectionShows {
				block.Heading, block.Items = "NOWE ODCINKI I SERIALE", n.NewShows
			}
			if len(block.Items) == 0 {
				// One note for an empty week, where the first catalog section is.
				if !quiet {
					continue
				}
				block.Quiet, quiet = true, false
			}
			for start := 0; start < len(block.Items); start += section.Columns {
				block.Rows = append(block.Rows, block.Items[start:min(start+section.Columns, len(block.Items))])
			}
		case config.SectionPopular:
			if len(n.Popular) == 0 {
				continue
			}
			block.Heading = "NAJPOPULARNIEJSZE NA SERWERZE"
		}
		result = append(result, block)
	}
	return result
}

// luminance returns the relative brightness (0-1) of a #rrggbb color.
func luminance(hex string) float64 {
	value, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 {
		return 0
	}
	r, g, b := float64(value>>16&0xff), float64(value>>8&0xff), float64(value&0xff)
	return (0.299*r + 0.587*g + 0.114*b) / 255
}

// HTML renders the email body. Images point at cid: references for email;
// with inline set they are embedded as data: URLs for a browser preview.
func HTML(n newsletter.Newsletter, inline bool) (string, error) {
	v := newView(n)
	v.ImageSrc = func(image *newsletter.Image) template.URL {
		if inline {
			return template.URL("data:" + image.Type + ";base64," + base64.StdEncoding.EncodeToString(image.Data))
		}
		return template.URL("cid:" + image.CID)
	}
	var output bytes.Buffer
	if err := htmlTemplates.ExecuteTemplate(&output, "email.html", v); err != nil {
		return "", err
	}
	return output.String(), nil
}

func Text(n newsletter.Newsletter) (string, error) {
	var output bytes.Buffer
	if err := textTemplates.ExecuteTemplate(&output, "email.txt", newView(n)); err != nil {
		return "", err
	}
	return output.String(), nil
}

// rows pairs each half-width block with the next one when that is half-width
// too; a half-width block without a partner takes the whole row.
func rows(blocks []Block) []Row {
	var result []Row
	for index := 0; index < len(blocks); index++ {
		if blocks[index].Width == "half" && index+1 < len(blocks) && blocks[index+1].Width == "half" {
			left, right := blocks[index], blocks[index+1]
			left.Half, right.Half = true, true
			result = append(result, Row{Blocks: []Block{left, right}})
			index++
			continue
		}
		result = append(result, Row{Blocks: []Block{blocks[index]}})
	}
	return result
}
