// Package delivery builds and sends newsletters: one test message or a run
// to every eligible account.
package delivery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/mail"
	"github.com/joloxx9/silo-newsletter/internal/newsletter"
	"github.com/joloxx9/silo-newsletter/internal/render"
	"github.com/joloxx9/silo-newsletter/internal/silo"
	"github.com/joloxx9/silo-newsletter/internal/state"
)

// ErrBusy means another delivery is already running.
var ErrBusy = errors.New("inna wysyłka właśnie trwa")

// Sender delivers one raw message; tests replace it.
type Sender func(cfg config.Config, to string, raw []byte) error

func SMTPSender(cfg config.Config, to string, raw []byte) error {
	server := mail.Server{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, Security: cfg.SMTPSecurity}
	return server.Send(cfg.FromEmail, to, raw)
}

type Service struct {
	Store *state.Store
	Log   *slog.Logger
	Send  Sender
	// Sleep waits between recipients; tests replace it.
	Sleep func(context.Context, time.Duration) error
	mu    sync.Mutex
}

func (s *Service) client(cfg config.Config) (*silo.Client, error) {
	return silo.New(cfg.SiloURL, cfg.SiloAPIKey)
}

// Recipient is an account with its delivery decision.
type Recipient struct {
	Account silo.Account
	Email   string
	Reason  string // why it is skipped, "" when it receives the newsletter
}

// Recipients lists every account with its delivery decision.
func Recipients(cfg config.Config, accounts []silo.Account) []Recipient {
	result := make([]Recipient, 0, len(accounts))
	for _, account := range accounts {
		recipient := Recipient{Account: account, Email: cfg.RecipientEmail(account.ID, account.Email)}
		switch {
		case !account.Active():
			recipient.Reason = "konto wyłączone w Silo"
		case cfg.UserExcluded(account.ID):
			recipient.Reason = "wykluczone w ustawieniach"
		case recipient.Email == "":
			recipient.Reason = "brak adresu e-mail"
		}
		result = append(result, recipient)
	}
	return result
}

// Preview builds one account's newsletter as browser HTML with inline images.
func (s *Service) Preview(ctx context.Context, cfg config.Config, accountID string) (string, error) {
	client, err := s.client(cfg)
	if err != nil {
		return "", err
	}
	account, err := findAccount(ctx, client, accountID)
	if err != nil {
		return "", err
	}
	builder, err := newsletter.NewBuilder(ctx, client, cfg, time.Now(), s.Log)
	if err != nil {
		return "", err
	}
	return render.HTML(builder.Build(ctx, account), true)
}

// SendTest sends one account's newsletter to TestEmail.
func (s *Service) SendTest(ctx context.Context, cfg config.Config, accountID string) error {
	if err := cfg.ValidateMail(); err != nil {
		return err
	}
	if !s.mu.TryLock() {
		return ErrBusy
	}
	defer s.mu.Unlock()
	client, err := s.client(cfg)
	if err != nil {
		return err
	}
	account, err := findAccount(ctx, client, accountID)
	if err != nil {
		return err
	}
	builder, err := newsletter.NewBuilder(ctx, client, cfg, time.Now(), s.Log)
	if err != nil {
		return err
	}
	raw, err := Compose(cfg, builder.Build(ctx, account), cfg.TestEmail, "[TEST] ")
	if err != nil {
		return err
	}
	return s.Send(cfg, cfg.TestEmail, raw)
}

// SendAll delivers to every eligible account and records the run.
func (s *Service) SendAll(ctx context.Context, cfg config.Config, kind string) (state.Run, error) {
	if !s.mu.TryLock() {
		return state.Run{}, ErrBusy
	}
	defer s.mu.Unlock()
	run := state.Run{ID: newID(), Kind: kind, StartedAt: time.Now().UTC(), Status: "running"}
	_ = s.Store.SaveRun(run)
	finish := func(err error) (state.Run, error) {
		run.FinishedAt = time.Now().UTC()
		if err != nil {
			run.Status, run.Message = "failed", err.Error()
		} else {
			run.Status = "done"
			if run.Failed > 0 {
				run.Status = "failed"
			}
		}
		if saveErr := s.Store.SaveRun(run); saveErr != nil {
			s.Log.Error("could not save delivery history", "error", saveErr)
		}
		s.Log.Info("delivery finished", "kind", kind, "sent", run.Sent, "failed", run.Failed, "skipped", run.Skipped)
		return run, err
	}
	if err := cfg.Validate(); err != nil {
		return finish(err)
	}
	if err := cfg.ValidateMail(); err != nil {
		return finish(err)
	}
	client, err := s.client(cfg)
	if err != nil {
		return finish(err)
	}
	accounts, err := client.Accounts(ctx)
	if err != nil {
		return finish(fmt.Errorf("lista kont: %w", err))
	}
	builder, err := newsletter.NewBuilder(ctx, client, cfg, time.Now(), s.Log)
	if err != nil {
		return finish(err)
	}
	first := true
	for _, recipient := range Recipients(cfg, accounts) {
		if recipient.Reason != "" {
			run.Skipped++
			continue
		}
		if !first && cfg.SendDelaySeconds > 0 {
			if err := s.Sleep(ctx, time.Duration(cfg.SendDelaySeconds)*time.Second); err != nil {
				return finish(err)
			}
		}
		first = false
		raw, err := Compose(cfg, builder.Build(ctx, recipient.Account), recipient.Email, "")
		if err == nil {
			err = s.Send(cfg, recipient.Email, raw)
		}
		if err != nil {
			run.Failed++
			run.Failures = append(run.Failures, recipient.Account.Username+": "+err.Error())
			s.Log.Warn("delivery failed", "account", recipient.Account.ID, "error", err)
		} else {
			run.Sent++
		}
		_ = s.Store.SaveRun(run)
	}
	return finish(nil)
}

// Compose renders the HTML, text, and inline images into one MIME message.
func Compose(cfg config.Config, n newsletter.Newsletter, to, subjectPrefix string) ([]byte, error) {
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
	return mail.Build(mail.Message{
		FromName: cfg.FromName, FromEmail: cfg.FromEmail, ReplyTo: cfg.ReplyToEmail, To: to,
		Subject: subjectPrefix + render.Subject(n), Text: text, HTML: html, Inline: inline,
	})
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
	return silo.Account{}, fmt.Errorf("nie ma konta Silo o ID %q", id)
}

func newID() string {
	buffer := make([]byte, 8)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}
