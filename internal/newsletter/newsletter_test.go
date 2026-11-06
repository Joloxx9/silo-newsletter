package newsletter_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/mail"
	"github.com/joloxx9/silo-newsletter/internal/newsletter"
	"github.com/joloxx9/silo-newsletter/internal/render"
	"github.com/joloxx9/silo-newsletter/internal/silo"
	"github.com/joloxx9/silo-newsletter/internal/silo/silotest"
)

var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T) (*silotest.Fake, *silo.Client, config.Config) {
	t.Helper()
	fake := silotest.New(t, now)
	client, err := silo.New(fake.URL(), silotest.Key)
	if err != nil {
		t.Fatal(err)
	}
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	cfg := config.Defaults()
	cfg.SiloURL = fake.URL()
	cfg.SiloAPIKey = silotest.Key
	cfg.ButtonURL = fake.URL() + "/"
	return fake, client, cfg
}

func build(t *testing.T, client *silo.Client, cfg config.Config, accountID string) newsletter.Newsletter {
	t.Helper()
	builder, err := newsletter.NewBuilder(context.Background(), client, cfg, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := client.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.ID == accountID {
			return builder.Build(context.Background(), account)
		}
	}
	t.Fatalf("account %s missing", accountID)
	return newsletter.Newsletter{}
}

func TestNewsletterForAlice(t *testing.T) {
	_, client, cfg := setup(t)
	n := build(t, client, cfg, "1")

	if len(n.NewMovies) != 2 || n.NewMovies[0].Name != "Fikcyjny Film" || n.NewMovies[1].Name != "Zgniły Pomidor" {
		t.Fatalf("new movies: %+v", n.NewMovies)
	}
	movie := n.NewMovies[0]
	if movie.IMDb != "7.8" || movie.RTCritic != 91 || movie.RTAudience != 85 || movie.Poster == nil || len(movie.Poster.Data) < 100 {
		t.Fatalf("movie detail: %+v", movie)
	}
	if string(movie.Poster.Data[:2]) != "\xff\xd8" {
		t.Fatal("poster was not converted to JPEG")
	}
	if len(n.NewShows) != 1 || n.NewShows[0].Name != "Serial Testowy" || n.NewShows[0].Detail != "2 nowe odcinki · sezon 1" {
		t.Fatalf("new shows: %+v", n.NewShows)
	}
	if n.NewShows[0].Poster != nil {
		t.Fatal("artwork from a foreign host was downloaded")
	}
	// Alice's kid profile is excluded from her statistics.
	if n.Personal.Plays != 2 || n.Personal.Movies != 1 || n.Personal.Episodes != 1 || n.Personal.TopShow != "Serial Testowy" {
		t.Fatalf("personal stats: %+v", n.Personal)
	}
	for _, recent := range n.Personal.Recent {
		if recent == "Bajka Dla Dzieci" {
			t.Fatal("child profile play counted")
		}
	}
	if len(n.Popular) != 1 || n.Popular[0].Name != "Fikcyjny Film" || n.Popular[0].Viewers != 2 {
		t.Fatalf("popular: %+v", n.Popular)
	}
}

func TestNewsletterIsolationAndLibraryAccess(t *testing.T) {
	_, client, cfg := setup(t)
	bob := build(t, client, cfg, "2")
	alice := build(t, client, cfg, "1")

	if len(bob.NewShows) != 0 {
		t.Fatal("Bob sees a series from a library he cannot access")
	}
	html, err := render.HTML(alice, false)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := render.Text(alice)
	for _, private := range []string{"Prywatny Seans Boba", "bob@example.com", "Bajka Dla Dzieci", silotest.Key} {
		if strings.Contains(html, private) || strings.Contains(text, private) {
			t.Fatalf("Alice's newsletter leaks %q", private)
		}
	}
}

func TestRenderPolishEmail(t *testing.T) {
	_, client, cfg := setup(t)
	n := build(t, client, cfg, "1")
	html, err := render.HTML(n, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cześć, alice!", "TWÓJ TYDZIEŃ", "NOWE FILMY", "NOWE ODCINKI I SERIALE", "31 października – 6 listopada 2026", "Otwórz Silo", "cid:poster-movie-tmdb-1001@silo-newsletter", "🍅 91%", "🤢 22%", ">film<", ">odcinek<", "2 osoby"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML is missing %q", want)
		}
	}
	if strings.Contains(html, "data:image") {
		t.Error("email HTML embeds data: images")
	}
	preview, _ := render.HTML(n, true)
	if out := os.Getenv("PREVIEW_OUT"); out != "" {
		_ = os.WriteFile(out, []byte(preview), 0o644)
	}
	if !strings.Contains(preview, "data:image/jpeg;base64,") {
		t.Error("preview does not inline images")
	}
	if subject := render.Subject(n); subject != "Nowości na Silo: 3 tytuły w tym tygodniu" {
		t.Errorf("subject: %q", subject)
	}

	raw, err := mail.Build(mail.Message{FromName: "Silo Newsletter", FromEmail: "news@example.com", To: "alice@example.com", Subject: render.Subject(n), HTML: html, Text: "x",
		Inline: []mail.Attachment{{CID: n.NewMovies[0].Poster.CID, ContentType: "image/jpeg", Data: n.NewMovies[0].Poster.Data}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"multipart/related", "multipart/alternative", "Content-ID: <poster-movie-tmdb-1001@silo-newsletter>", "Subject: =?utf-8?q?"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("MIME is missing %q", want)
		}
	}
}

func TestQuietWeek(t *testing.T) {
	_, client, cfg := setup(t)
	cfg.DaysBack = 1
	cfg.IncludedLibraryIDs = []string{"99"}
	n := build(t, client, cfg, "4")
	html, err := render.HTML(n, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.NewMovies)+len(n.NewShows) != 0 || !strings.Contains(html, "nie pojawiło się nic nowego") || !strings.Contains(html, "nie było seansów") {
		t.Fatalf("quiet week rendering: movies=%d shows=%d", len(n.NewMovies), len(n.NewShows))
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "filmów", 1: "film", 2: "filmy", 4: "filmy", 5: "filmów", 12: "filmów", 22: "filmy", 25: "filmów", 104: "filmy"} {
		if got := newsletter.Plural(n, "film", "filmy", "filmów"); got != want {
			t.Errorf("Plural(%d) = %s, want %s", n, got, want)
		}
	}
}
