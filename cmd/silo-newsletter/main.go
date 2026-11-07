// Command silo-newsletter builds and sends weekly newsletters for a Silo
// media server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/delivery"
	"github.com/joloxx9/silo-newsletter/internal/newsletter"
	"github.com/joloxx9/silo-newsletter/internal/render"
	"github.com/joloxx9/silo-newsletter/internal/schedule"
	"github.com/joloxx9/silo-newsletter/internal/silo"
	"github.com/joloxx9/silo-newsletter/internal/state"
	"github.com/joloxx9/silo-newsletter/internal/web"
)

var version = "dev"

const usage = `Silo Newsletter %s

Użycie:
  silo-newsletter verify                 sprawdza połączenie z Silo
  silo-newsletter users                  lista kont Silo
  silo-newsletter preview <ID konta>     zapisuje podgląd HTML (bez wysyłki)
  silo-newsletter send-test <ID konta>   wysyła newsletter tego konta na TestEmail
  silo-newsletter run                    panel WWW (port 8080) i cotygodniowy harmonogram

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
		if err := serve(ctx, log); err != nil {
			log.Error("Silo Newsletter stopped", "error", err)
			os.Exit(1)
		}
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
	raw, err := delivery.Compose(cfg, n, cfg.TestEmail, "[TEST] ")
	if err != nil {
		return err
	}
	if err := delivery.SMTPSender(cfg, cfg.TestEmail, raw); err != nil {
		return err
	}
	fmt.Printf("Wysłano test na TestEmail (%d KB).\n", len(raw)/1024)
	return nil
}

// serve runs the settings panel and the weekly scheduler until ctx ends.
func serve(ctx context.Context, log *slog.Logger) error {
	path := config.Path()
	directory := filepath.Dir(path)
	if probe, err := os.CreateTemp(directory, ".write-test-*"); err != nil {
		log.Error("katalog danych nie jest zapisywalny; na Unraid uruchom: chown -R 99:100 /mnt/user/appdata/silo-newsletter", "katalog", directory)
	} else {
		probe.Close()
		os.Remove(probe.Name())
	}
	store, err := state.Open(filepath.Join(directory, "state.json"))
	if err != nil {
		return err
	}
	service := &delivery.Service{Store: store, Log: log, Send: delivery.SMTPSender, Sleep: sleepContext}
	server := &web.Server{ConfigPath: path, Store: store, Delivery: service, Log: log, Version: version, Background: ctx}
	handler := server.Handler()
	address := ":" + envOr("PORT", "8080")
	log.Info("Silo Newsletter działa", "wersja", version, "panel", address, "konfiguracja", path)

	runner := schedule.Runner{
		LoadConfig: func() (config.Config, error) {
			cfg, err := config.LoadFile(path)
			return config.Effective(cfg), err
		},
		LastSlot:  store.LastSlot,
		ClaimSlot: store.ClaimSlot,
		SendAll: func(ctx context.Context, cfg config.Config) error {
			_, err := service.SendAll(ctx, cfg, "scheduled")
			return err
		},
		Log: log,
	}
	go runner.Run(ctx)

	httpServer := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	errs := make(chan error, 1)
	go func() { errs <- httpServer.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	case err := <-errs:
		return err
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
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
