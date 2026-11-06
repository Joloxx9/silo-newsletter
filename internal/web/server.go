// Package web serves the Silo Newsletter settings panel.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base32"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/delivery"
	"github.com/joloxx9/silo-newsletter/internal/schedule"
	"github.com/joloxx9/silo-newsletter/internal/silo"
	"github.com/joloxx9/silo-newsletter/internal/state"
)

//go:embed assets/*
var assets embed.FS

const (
	sessionCookie   = "silo_newsletter_session"
	sessionLifetime = 12 * time.Hour
	maxBody         = 1 << 20
)

type Server struct {
	ConfigPath string
	Store      *state.Store
	Delivery   *delivery.Service
	Log        *slog.Logger
	Version    string
	// Background is the context for deliveries started from the panel.
	Background context.Context

	setupCode string
	configMu  sync.Mutex

	sessionsMu sync.Mutex
	sessions   map[string]time.Time

	loginMu       sync.Mutex
	failedLogins  int
	lockedUntil   time.Time
	sendAllActive bool
}

// SetupCode returns the one-time code needed to set the first password. It
// is printed to the container log only, never served over HTTP.
func (s *Server) SetupCode() string { return s.setupCode }

func (s *Server) Handler() http.Handler {
	if s.sessions == nil {
		s.sessions = map[string]time.Time{}
	}
	if !s.Store.HasPassword() && s.setupCode == "" {
		s.setupCode = randomToken(10)
	}
	static, _ := fs.Sub(assets, "assets")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/config", s.auth(s.handleGetConfig))
	mux.HandleFunc("PUT /api/config", s.auth(s.handlePutConfig))
	mux.HandleFunc("POST /api/test/silo", s.auth(s.handleTestSilo))
	mux.HandleFunc("GET /api/silo/users", s.auth(s.handleUsers))
	mux.HandleFunc("GET /api/silo/libraries", s.auth(s.handleLibraries))
	mux.HandleFunc("POST /api/preview", s.auth(s.handlePreview))
	mux.HandleFunc("POST /api/send-test", s.auth(s.handleSendTest))
	mux.HandleFunc("POST /api/send-all", s.auth(s.handleSendAll))
	mux.HandleFunc("GET /api/history", s.auth(s.handleHistory))
	mux.HandleFunc("POST /api/password", s.auth(s.handlePassword))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			header.Set("Cache-Control", "no-store")
			// State-changing calls must come from the panel's own scripts.
			if r.Method != http.MethodGet && r.Header.Get("X-Silo-Newsletter") != "1" {
				writeError(w, http.StatusForbidden, "brak nagłówka panelu")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- sessions and login ----------

func randomToken(bytes int) string {
	buffer := make([]byte, bytes)
	_, _ = rand.Read(buffer)
	return strings.TrimRight(base32.StdEncoding.EncodeToString(buffer), "=")
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request) {
	token := randomToken(32)
	s.sessionsMu.Lock()
	now := time.Now()
	for existing, expires := range s.sessions {
		if now.After(expires) {
			delete(s.sessions, existing)
		}
	}
	s.sessions[token] = now.Add(sessionLifetime)
	s.sessionsMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: int(sessionLifetime.Seconds())})
}

func (s *Server) loggedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()
	expires, ok := s.sessions[cookie.Value]
	if !ok || time.Now().After(expires) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			writeError(w, http.StatusUnauthorized, "zaloguj się")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{"setupRequired": !s.Store.HasPassword(), "loggedIn": s.loggedIn(r), "version": s.Version}
	if s.loggedIn(r) {
		cfg := s.effectiveConfig()
		response["configured"] = cfg.Validate() == nil
		response["mailConfigured"] = cfg.ValidateMail() == nil
		response["scheduleEnabled"] = cfg.ScheduleEnabled
		if next, ok := schedule.Next(time.Now(), cfg); ok && cfg.ScheduleEnabled {
			response["nextSend"] = next.Format(time.RFC3339)
		}
		response["sending"] = s.isSending()
		if runs := s.Store.Runs(); len(runs) > 0 {
			response["lastRun"] = runs[0]
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var request struct{ Code, Password string }
	if !decode(w, r, &request) {
		return
	}
	if s.Store.HasPassword() {
		writeError(w, http.StatusConflict, "hasło jest już ustawione")
		return
	}
	if !s.allowLoginAttempt() {
		writeError(w, http.StatusTooManyRequests, "za dużo prób, odczekaj chwilę")
		return
	}
	code := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(request.Code), " ", ""))
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.setupCode)) != 1 {
		s.loginFailed()
		writeError(w, http.StatusForbidden, "nieprawidłowy kod z logów kontenera")
		return
	}
	if err := s.Store.SetPassword(request.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.setupCode = ""
	s.loginSucceeded()
	s.newSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) allowLoginAttempt() bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	return time.Now().After(s.lockedUntil)
}

func (s *Server) loginFailed() {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.failedLogins++
	if s.failedLogins >= 5 {
		s.lockedUntil = time.Now().Add(time.Minute)
		s.failedLogins = 0
	}
}

func (s *Server) loginSucceeded() {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	s.failedLogins = 0
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct{ Password string }
	if !decode(w, r, &request) {
		return
	}
	if !s.allowLoginAttempt() {
		writeError(w, http.StatusTooManyRequests, "za dużo prób, odczekaj minutę")
		return
	}
	if !s.Store.CheckPassword(request.Password) {
		s.loginFailed()
		writeError(w, http.StatusForbidden, "nieprawidłowe hasło")
		return
	}
	s.loginSucceeded()
	s.newSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.sessionsMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var request struct{ Current, Password string }
	if !decode(w, r, &request) {
		return
	}
	if !s.Store.CheckPassword(request.Current) {
		writeError(w, http.StatusForbidden, "obecne hasło jest nieprawidłowe")
		return
	}
	if err := s.Store.SetPassword(request.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.sessionsMu.Lock()
	s.sessions = map[string]time.Time{}
	s.sessionsMu.Unlock()
	s.newSession(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- configuration ----------

// configView is the file configuration with secrets replaced by flags.
type configView struct {
	config.Config
	HasSiloAPIKey   bool `json:"HasSiloApiKey"`
	HasSMTPPassword bool `json:"HasSmtpPassword"`
	Problem         string
	MailProblem     string
	Weekdays        []string
}

func (s *Server) fileConfig() (config.Config, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return config.LoadFile(s.ConfigPath)
}

func (s *Server) effectiveConfig() config.Config {
	cfg, err := s.fileConfig()
	if err != nil {
		cfg = config.Defaults()
	}
	return config.Effective(cfg)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	cfg, err := s.fileConfig()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := configView{Config: cfg, HasSiloAPIKey: cfg.SiloAPIKey != "", HasSMTPPassword: cfg.SMTPPassword != "", Weekdays: config.Weekdays}
	effective := config.Effective(cfg)
	if err := effective.Validate(); err != nil {
		view.Problem = err.Error()
	}
	if err := effective.ValidateMail(); err != nil {
		view.MailProblem = err.Error()
	}
	view.SiloAPIKey, view.SMTPPassword = "", ""
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var incoming config.Config
	if !decode(w, r, &incoming) {
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	current, err := config.LoadFile(s.ConfigPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Empty secret fields keep the stored value.
	if strings.TrimSpace(incoming.SiloAPIKey) == "" {
		incoming.SiloAPIKey = current.SiloAPIKey
	}
	if incoming.SMTPPassword == "" {
		incoming.SMTPPassword = current.SMTPPassword
	}
	incoming.SiloURL = strings.TrimRight(strings.TrimSpace(incoming.SiloURL), "/")
	incoming.SiloAPIKey = strings.TrimSpace(incoming.SiloAPIKey)
	if incoming.EmailOverrides != nil {
		for id, address := range incoming.EmailOverrides {
			if strings.TrimSpace(address) == "" {
				delete(incoming.EmailOverrides, id)
			} else {
				incoming.EmailOverrides[id] = strings.TrimSpace(address)
			}
		}
	}
	if err := incoming.ValidateSchedule(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if incoming.SiloURL != "" {
		probe := incoming
		if probe.SiloAPIKey == "" {
			probe.SiloAPIKey = "placeholder"
		}
		if err := config.Effective(probe).Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	scheduleChanged := incoming.ScheduleEnabled && (!current.ScheduleEnabled || incoming.ScheduleDay != current.ScheduleDay ||
		incoming.ScheduleTime != current.ScheduleTime || incoming.TimeZone != current.TimeZone)
	if err := config.Save(s.ConfigPath, incoming); err != nil {
		writeError(w, http.StatusInternalServerError, "nie udało się zapisać konfiguracji: "+err.Error())
		return
	}
	if scheduleChanged {
		// Turning the schedule on (or moving it) never fires a slot that
		// already passed; the first send is the next upcoming one.
		if slot, ok := schedule.Slot(time.Now(), config.Effective(incoming)); ok {
			_, _ = s.Store.ClaimSlot(slot)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- Silo, preview, delivery ----------

func (s *Server) siloClient() (*silo.Client, config.Config, error) {
	cfg := s.effectiveConfig()
	if err := cfg.Validate(); err != nil {
		return nil, cfg, err
	}
	client, err := silo.New(cfg.SiloURL, cfg.SiloAPIKey)
	return client, cfg, err
}

func requestContext(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func (s *Server) handleTestSilo(w http.ResponseWriter, r *http.Request) {
	client, _, err := s.siloClient()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := requestContext(r, time.Minute)
	defer cancel()
	accounts, libraries, err := client.Check(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accounts": accounts, "libraries": libraries})
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	client, cfg, err := s.siloClient()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := requestContext(r, time.Minute)
	defer cancel()
	accounts, err := client.Accounts(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	type user struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Active        bool   `json:"active"`
		Admin         bool   `json:"admin"`
		HasSiloEmail  bool   `json:"hasSiloEmail"`
		EmailOverride string `json:"emailOverride"`
		Excluded      bool   `json:"excluded"`
		Reason        string `json:"reason"`
	}
	users := []user{}
	for _, recipient := range delivery.Recipients(cfg, accounts) {
		account := recipient.Account
		users = append(users, user{
			ID: account.ID, Name: account.Username, Active: account.Active(), Admin: account.Role == "admin",
			HasSiloEmail: account.Email != "", EmailOverride: cfg.EmailOverrides[account.ID],
			Excluded: cfg.UserExcluded(account.ID), Reason: recipient.Reason,
		})
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	client, _, err := s.siloClient()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := requestContext(r, time.Minute)
	defer cancel()
	libraries, err := client.Libraries(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, libraries)
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	var request struct{ UserID string }
	if !decode(w, r, &request) {
		return
	}
	_, cfg, err := s.siloClient()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := requestContext(r, 3*time.Minute)
	defer cancel()
	html, err := s.Delivery.Preview(ctx, cfg, request.UserID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

func (s *Server) handleSendTest(w http.ResponseWriter, r *http.Request) {
	var request struct{ UserID string }
	if !decode(w, r, &request) {
		return
	}
	_, cfg, err := s.siloClient()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := requestContext(r, 3*time.Minute)
	defer cancel()
	if err := s.Delivery.SendTest(ctx, cfg, request.UserID); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, delivery.ErrBusy) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sentTo": maskEmail(cfg.TestEmail)})
}

func (s *Server) isSending() bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	return s.sendAllActive
}

func (s *Server) handleSendAll(w http.ResponseWriter, r *http.Request) {
	var request struct{ Confirm string }
	if !decode(w, r, &request) {
		return
	}
	if request.Confirm != "WYŚLIJ" {
		writeError(w, http.StatusBadRequest, "potwierdź wysyłkę do wszystkich")
		return
	}
	cfg := s.effectiveConfig()
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cfg.ValidateMail(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.loginMu.Lock()
	if s.sendAllActive {
		s.loginMu.Unlock()
		writeError(w, http.StatusConflict, delivery.ErrBusy.Error())
		return
	}
	s.sendAllActive = true
	s.loginMu.Unlock()
	background := s.Background
	if background == nil {
		background = context.Background()
	}
	go func() {
		defer func() {
			s.loginMu.Lock()
			s.sendAllActive = false
			s.loginMu.Unlock()
		}()
		if _, err := s.Delivery.SendAll(background, cfg, "manual"); err != nil {
			s.Log.Error("manual delivery failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleHistory(w http.ResponseWriter, _ *http.Request) {
	runs := s.Store.Runs()
	if runs == nil {
		runs = []state.Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

// ---------- helpers ----------

func maskEmail(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 1 {
		return "***"
	}
	return address[:1] + "***" + address[at:]
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "nieprawidłowe dane: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
