// Command silo-newsletter builds and sends weekly newsletters for a Silo
// media server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/mail"
	"github.com/joloxx9/silo-newsletter/internal/newsletter"
	"github.com/joloxx9/silo-newsletter/internal/render"
	"github.com/joloxx9/silo-newsletter/internal/silo"
)

var version = "dev"

const usage = `Silo Newsletter %s

Użycie:
  silo-newsletter verify                 sprawdza połączenie z Silo
  silo-newsletter users                  lista kont Silo
  silo-newsletter preview <ID konta>     zapisuje podgląd HTML (bez wysyłki)
  silo-newsletter send-test <ID konta>   wysyła newsletter tego konta na TestEmail
  silo-newsletter run                    tryb kontenera (harmonogram: etap 2)

Konfiguracja: %s (zmienna SILO_NEWSLETTER_CONFIG zmienia ścieżkę).
`

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "-h" || os.Args[1] == "--help" {
		fmt.Printf(usage, version, config.Path())
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Args[1] == "run" {
		// Stage 1 container mode: stay up so commands can be run with
		// docker exec. The weekly scheduler replaces this in stage 2.
		log.Info("Silo Newsletter działa", "wersja", version, "konfiguracja", config.Path())
		log.Info("Użyj: docker exec silo-newsletter silo-newsletter verify | users | preview <ID> | send-test <ID>")
		<-ctx.Done()
		return
	}
	if err := run(ctx, os.Args[1], os.Args[2:], log); err != nil {
		fmt.Fprintln(os.Stderr, "Błąd:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, command string, args []string, log *slog.Logger) error {
	cfg, err := config.Load(config.Path())
	if err != nil {
		return err
	}
	client, err := silo.New(cfg.SiloURL, cfg.SiloAPIKey)
	if err != nil {
		return err
	}
	switch command {
	case "verify":
		accounts, libraries, err := client.Check(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("OK: klucz działa. Konta: %d, biblioteki: %d; historia, katalog, oceny i plakaty są dostępne.\n", accounts, libraries)
		return nil
	case "users":
		accounts, err := client.Accounts(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("%-8s %-24s %-9s %s\n", "ID", "Nazwa", "Aktywne", "E-mail")
		for _, account := range accounts {
			email := "brak"
			if account.Email != "" {
				email = "jest"
			}
			active := "tak"
			if !account.Active() {
				active = "nie"
			}
			if cfg.UserExcluded(account.ID) {
				active += " (wykluczone)"
			}
			fmt.Printf("%-8s %-24s %-9s %s\n", account.ID, account.Username, active, email)
		}
		return nil
	case "preview", "send-test":
		flags := flag.NewFlagSet(command, flag.ContinueOnError)
		output := flags.String("o", "/data/output", "katalog na podgląd")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if flags.NArg() != 1 {
			return fmt.Errorf("podaj ID konta, np. silo-newsletter %s 1", command)
		}
		if command == "send-test" {
			if err := cfg.ValidateMail(); err != nil {
				return err
			}
		}
		account, err := findAccount(ctx, client, flags.Arg(0))
		if err != nil {
			return err
		}
		builder, err := newsletter.NewBuilder(ctx, client, cfg, time.Now(), log)
		if err != nil {
			return err
		}
		n := builder.Build(ctx, account)
		if command == "preview" {
			return writePreview(n, *output)
		}
		return sendTest(cfg, n)
	default:
		return fmt.Errorf("nieznane polecenie %q; uruchom silo-newsletter help", command)
	}
}

func findAccount(ctx context.Context, client *silo.Client, id string) (silo.Account, error) {
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return silo.Account{}, err
	}
	for _, account := range accounts {
		if account.ID == id {
			return account, nil
		}
	}
	return silo.Account{}, fmt.Errorf("nie ma konta Silo o ID %q; sprawdź silo-newsletter users", id)
}

func writePreview(n newsletter.Newsletter, directory string) error {
	html, err := render.HTML(n, true)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	path := filepath.Join(directory, "podglad-"+n.RecipientID+".html")
	if err := os.WriteFile(path, []byte(html), 0o640); err != nil {
		return err
	}
	fmt.Printf("Podgląd zapisany: %s (filmy: %d, seriale: %d)\n", path, len(n.NewMovies), len(n.NewShows))
	return nil
}

func sendTest(cfg config.Config, n newsletter.Newsletter) error {
	raw, err := compose(cfg, n, cfg.TestEmail, "[TEST] ")
	if err != nil {
		return err
	}
	server := mail.Server{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, Security: cfg.SMTPSecurity}
	if err := server.Send(cfg.FromEmail, cfg.TestEmail, raw); err != nil {
		return err
	}
	fmt.Printf("Wysłano test na TestEmail (%d KB).\n", len(raw)/1024)
	return nil
}

func compose(cfg config.Config, n newsletter.Newsletter, to, subjectPrefix string) ([]byte, error) {
	html, err := render.HTML(n, false)
	if err != nil {
		return nil, err
	}
	text, err := render.Text(n)
	if err != nil {
		return nil, err
	}
	var inline []mail.Attachment
	for _, image := range n.Images() {
		inline = append(inline, mail.Attachment{CID: image.CID, ContentType: "image/jpeg", Data: image.Data})
	}
	if to == "" {
		return nil, errors.New("no recipient")
	}
	return mail.Build(mail.Message{
		FromName: cfg.FromName, FromEmail: cfg.FromEmail, ReplyTo: cfg.ReplyToEmail, To: to,
		Subject: subjectPrefix + render.Subject(n), Text: text, HTML: html, Inline: inline,
	})
}
