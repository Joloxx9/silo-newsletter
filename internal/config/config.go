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
}

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
	applyEnv(&cfg)
	cfg.normalize()
	return cfg, cfg.Validate()
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
	if strings.TrimSpace(c.ButtonURL) == "" {
		c.ButtonURL = c.SiloURL + "/"
	}
}

// Validate reports the first problem that would stop a newsletter run.
func (c Config) Validate() error {
	parsed, err := url.Parse(c.SiloURL)
	if c.SiloURL == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("SiloUrl must be an http(s) address such as http://192.168.1.10:8080")
	}
	if c.SiloAPIKey == "" || strings.ContainsAny(c.SiloAPIKey, " \t\r\n") || len(c.SiloAPIKey) > 512 {
		return errors.New("SiloApiKey must be one Silo API key")
	}
	if _, err := time.LoadLocation(c.TimeZone); err != nil {
		return fmt.Errorf("TimeZone %q is not a known time zone", c.TimeZone)
	}
	if c.DaysBack < 1 || c.DaysBack > 60 {
		return errors.New("DaysBack must be between 1 and 60")
	}
	if c.MaxMovies < 0 || c.MaxMovies > 50 || c.MaxShows < 0 || c.MaxShows > 50 {
		return errors.New("MaxMovies and MaxShows must be between 0 and 50")
	}
	switch c.SMTPSecurity {
	case "starttls", "tls", "none":
	default:
		return errors.New(`SmtpSecurity must be "starttls", "tls", or "none"`)
	}
	return nil
}

// ValidateMail checks the settings needed only when sending email.
func (c Config) ValidateMail() error {
	if c.SMTPHost == "" || strings.ContainsAny(c.SMTPHost, ":/ ") {
		return errors.New("SmtpHost must be a host name such as smtp.gmail.com")
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return errors.New("SmtpPort must be between 1 and 65535")
	}
	for name, value := range map[string]string{"FromEmail": c.FromEmail, "TestEmail": c.TestEmail} {
		if _, err := mail.ParseAddress(value); err != nil || strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must be an email address", name)
		}
	}
	if c.ReplyToEmail != "" {
		if _, err := mail.ParseAddress(c.ReplyToEmail); err != nil {
			return errors.New("ReplyToEmail must be an email address")
		}
	}
	if (c.SMTPUsername == "") != (c.SMTPPassword == "") {
		return errors.New("set both SmtpUsername and SmtpPassword, or neither")
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
