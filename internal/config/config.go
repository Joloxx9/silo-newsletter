// Package config loads Silo Newsletter settings from a JSON file with
// environment-variable overrides.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultPath is the configuration file inside the container data volume.
const DefaultPath = "/data/config.json"

type Config struct {
	SiloURL    string `json:"SiloUrl"`
	SiloAPIKey string `json:"SiloApiKey"`

	ServerName string `json:"ServerName"`
	ButtonURL  string `json:"ButtonUrl"`

	FromName     string `json:"FromName"`
	FromEmail    string `json:"FromEmail"`
	ReplyToEmail string `json:"ReplyToEmail"`
	TestEmail    string `json:"TestEmail"`

	SMTPHost     string `json:"SmtpHost"`
	SMTPPort     int    `json:"SmtpPort"`
	SMTPUsername string `json:"SmtpUsername"`
	SMTPPassword string `json:"SmtpPassword"`
	// SMTPSecurity is "starttls" (default), "tls" (implicit, port 465), or "none".
	SMTPSecurity string `json:"SmtpSecurity"`

	TimeZone             string   `json:"TimeZone"`
	DaysBack             int      `json:"DaysBack"`
	MaxMovies            int      `json:"MaxMovies"`
	MaxShows             int      `json:"MaxShows"`
	IncludeChildProfiles bool     `json:"IncludeChildProfiles"`
	ExcludedUserIDs      []string `json:"ExcludedUserIds"`
	IncludedLibraryIDs   []string `json:"IncludedLibraryIds"`
	// EmailOverrides gives an address to accounts without one in Silo
	// (keyed by Silo account ID). A Silo address always wins.
	EmailOverrides map[string]string `json:"EmailOverrides"`

	ScheduleEnabled  bool   `json:"ScheduleEnabled"`
	ScheduleDay      string `json:"ScheduleDay"`  // Monday ... Sunday
	ScheduleTime     string `json:"ScheduleTime"` // HH:MM, local TimeZone
	SendDelaySeconds int    `json:"SendDelaySeconds"`
}

var Weekdays = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

func Defaults() Config {
	return Config{
		ServerName:   "Silo",
		FromName:     "Silo Newsletter",
		SMTPPort:     587,
		SMTPSecurity: "starttls",
		TimeZone:     "Europe/Warsaw",
		DaysBack:     7,
		MaxMovies:    8,
		MaxShows:     8,

		ScheduleDay:      "Friday",
		ScheduleTime:     "09:30",
		SendDelaySeconds: 5,
	}
}

// Path returns the configuration path from SILO_NEWSLETTER_CONFIG or the default.
func Path() string {
	if value := strings.TrimSpace(os.Getenv("SILO_NEWSLETTER_CONFIG")); value != "" {
		return value
	}
	return DefaultPath
}

// Load reads path (a missing file is allowed when the environment supplies
// everything), applies environment overrides, and validates the result.
func Load(path string) (Config, error) {
	cfg, err := LoadFile(path)
	if err != nil {
		return Config{}, err
	}
	applyEnv(&cfg)
	cfg.normalize()
	return cfg, cfg.Validate()
}

// LoadFile reads only the file (defaults for a missing file), without
// environment overrides or validation; the web panel edits this view.
func LoadFile(path string) (Config, error) {
	cfg := Defaults()
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("config %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Effective applies environment overrides and normalization to a file view.
func Effective(cfg Config) Config {
	applyEnv(&cfg)
	cfg.normalize()
	return cfg
}

// Save writes the file atomically with owner-only permissions.
func Save(path string, cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// Button is the newsletter button address: ButtonUrl or the Silo address.
func (c Config) Button() string {
	if strings.TrimSpace(c.ButtonURL) != "" {
		return strings.TrimSpace(c.ButtonURL)
	}
	return strings.TrimRight(c.SiloURL, "/") + "/"
}

// RecipientEmail returns the delivery address for an account.
func (c Config) RecipientEmail(accountID, siloEmail string) string {
	if strings.TrimSpace(siloEmail) != "" {
		return strings.TrimSpace(siloEmail)
	}
	return strings.TrimSpace(c.EmailOverrides[accountID])
}

func applyEnv(cfg *Config) {
	text := map[string]*string{
		"SILO_URL":       &cfg.SiloURL,
		"SILO_API_KEY":   &cfg.SiloAPIKey,
		"SMTP_HOST":      &cfg.SMTPHost,
		"SMTP_USERNAME":  &cfg.SMTPUsername,
		"SMTP_PASSWORD":  &cfg.SMTPPassword,
		"SMTP_SECURITY":  &cfg.SMTPSecurity,
		"FROM_EMAIL":     &cfg.FromEmail,
		"FROM_NAME":      &cfg.FromName,
		"TEST_EMAIL":     &cfg.TestEmail,
		"NEWSLETTER_TZ":  &cfg.TimeZone,
		"SERVER_NAME":    &cfg.ServerName,
		"REPLY_TO_EMAIL": &cfg.ReplyToEmail,
	}
	for name, target := range text {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if value, ok := os.LookupEnv("SMTP_PORT"); ok {
		if port, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			cfg.SMTPPort = port
		}
	}
}

func (c *Config) normalize() {
	c.SiloURL = strings.TrimRight(strings.TrimSpace(c.SiloURL), "/")
	c.SiloAPIKey = strings.TrimSpace(c.SiloAPIKey)
	c.SMTPHost = strings.TrimSpace(c.SMTPHost)
	c.SMTPSecurity = strings.ToLower(strings.TrimSpace(c.SMTPSecurity))
	if c.SMTPSecurity == "" {
		c.SMTPSecurity = "starttls"
	}
	if strings.TrimSpace(c.ServerName) == "" {
		c.ServerName = "Silo"
	}
}

// Validate reports the first problem that would stop a newsletter run.
func (c Config) Validate() error {
	parsed, err := url.Parse(c.SiloURL)
	if c.SiloURL == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("adres Silo musi zaczynać się od http:// lub https://, np. http://192.168.1.10:8080")
	}
	if c.SiloAPIKey == "" || strings.ContainsAny(c.SiloAPIKey, " \t\r\n") || len(c.SiloAPIKey) > 512 {
		return errors.New("wpisz klucz API Silo")
	}
	if _, err := time.LoadLocation(c.TimeZone); err != nil {
		return fmt.Errorf("nieznana strefa czasowa %q (np. Europe/Warsaw)", c.TimeZone)
	}
	if c.DaysBack < 1 || c.DaysBack > 60 {
		return errors.New("liczba dni musi być od 1 do 60")
	}
	if c.MaxMovies < 0 || c.MaxMovies > 50 || c.MaxShows < 0 || c.MaxShows > 50 {
		return errors.New("maksymalna liczba filmów i seriali musi być od 0 do 50")
	}
	switch c.SMTPSecurity {
	case "starttls", "tls", "none":
	default:
		return errors.New("wybierz rodzaj szyfrowania SMTP")
	}
	return c.ValidateSchedule()
}

func (c Config) ValidateSchedule() error {
	known := false
	for _, day := range Weekdays {
		known = known || day == c.ScheduleDay
	}
	if !known {
		return errors.New("wybierz dzień wysyłki")
	}
	if parsed, err := time.Parse("15:04", c.ScheduleTime); err != nil || parsed.Format("15:04") != c.ScheduleTime {
		return errors.New("godzina wysyłki musi mieć format GG:MM, np. 09:30")
	}
	if c.SendDelaySeconds < 0 || c.SendDelaySeconds > 600 {
		return errors.New("przerwa między e-mailami musi być od 0 do 600 sekund")
	}
	for id, address := range c.EmailOverrides {
		if parsed, err := mail.ParseAddress(address); err != nil || parsed.Address != strings.TrimSpace(address) {
			return fmt.Errorf("adres zastępczy dla konta %s jest niepoprawny", id)
		}
	}
	return nil
}

// ValidateMail checks the settings needed only when sending email.
func (c Config) ValidateMail() error {
	if c.SMTPHost == "" || strings.ContainsAny(c.SMTPHost, ":/ ") {
		return errors.New("podaj serwer SMTP, np. smtp.gmail.com")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return errors.New("port SMTP musi być od 1 do 65535")
	}
	for name, value := range map[string]string{"FromEmail": c.FromEmail, "TestEmail": c.TestEmail} {
		if _, err := mail.ParseAddress(value); err != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s: wpisz poprawny adres e-mail", map[string]string{"FromEmail": "adres nadawcy", "TestEmail": "adres testowy"}[name])
		}
	}
	if c.ReplyToEmail != "" {
		if _, err := mail.ParseAddress(c.ReplyToEmail); err != nil {
			return errors.New("adres do odpowiedzi jest niepoprawny")
		}
	}
	if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
		return errors.New("podaj użytkownika i hasło SMTP (albo żadne z nich)")
	}
	return nil
}

// Location returns the newsletter time zone.
func (c Config) Location() *time.Location {
	location, err := time.LoadLocation(c.TimeZone)
	if err != nil {
		return time.UTC
	}
	return location
}

func (c Config) UserExcluded(id string) bool {
	for _, excluded := range c.ExcludedUserIDs {
		if strings.TrimSpace(excluded) == id {
			return true
		}
	}
	return false
}
