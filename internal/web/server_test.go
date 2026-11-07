package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
	"github.com/joloxx9/silo-newsletter/internal/delivery"
	"github.com/joloxx9/silo-newsletter/internal/silo/silotest"
	"github.com/joloxx9/silo-newsletter/internal/state"
)

type sent struct {
	to  string
	raw string
}

type harness struct {
	t       *testing.T
	server  *Server
	handler http.Handler
	fake    *silotest.Fake
	path    string
	mu      sync.Mutex
	sent    []sent
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	directory := t.TempDir()
	store, err := state.Open(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fake: silotest.New(t, time.Now()), path: filepath.Join(directory, "config.json")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := &delivery.Service{Store: store, Log: log, Sleep: func(context.Context, time.Duration) error { return nil },
		Send: func(_ config.Config, to string, raw []byte) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.sent = append(h.sent, sent{to: to, raw: string(raw)})
			return nil
		}}
	h.server = &Server{ConfigPath: h.path, Store: store, Delivery: service, Log: log, Version: "test"}
	h.handler = h.server.Handler()
	return h
}

func (h *harness) call(method, path string, body any, panelHeader bool) (int, map[string]any, []byte) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, reader)
	if panelHeader {
		request.Header.Set("X-Silo-Newsletter", "1")
	}
	recorder := httptest.NewRecorder()
	h.handler.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded, recorder.Body.Bytes()
}

func (h *harness) configure() {
	h.t.Helper()
	cfg := config.Defaults()
	cfg.SiloURL = h.fake.URL()
	cfg.SiloAPIKey = silotest.Key
	cfg.SMTPHost, cfg.SMTPPort = "smtp.example.com", 587
	cfg.SMTPUsername, cfg.SMTPPassword = "news@example.com", "smtp-secret-value"
	cfg.FromEmail, cfg.TestEmail = "news@example.com", "admin@example.com"
	cfg.EmailOverrides = map[string]string{"4": "dave.fallback@example.com"}
	if code, body, _ := h.call("PUT", "/api/config", cfg, true); code != 200 {
		h.t.Fatalf("save config: %d %v", code, body)
	}
}

func TestPanelNeedsNoLoginButChangesNeedPanelHeader(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.call("GET", "/api/config", nil, false); code != http.StatusOK {
		t.Fatalf("config without login: %d", code)
	}
	if code, _, _ := h.call("PUT", "/api/config", config.Defaults(), false); code != http.StatusForbidden {
		t.Fatalf("change without panel header: %d", code)
	}
}

func TestConfigSecretsAreNeverReturnedAndKeptWhenBlank(t *testing.T) {
	h := newHarness(t)
	h.configure()
	code, view, raw := h.call("GET", "/api/config", nil, true)
	if code != 200 || view["HasSiloApiKey"] != true || view["HasSmtpPassword"] != true || view["Problem"] != "" {
		t.Fatalf("config view: %d %v", code, view)
	}
	if strings.Contains(string(raw), silotest.Key) || strings.Contains(string(raw), "smtp-secret-value") {
		t.Fatal("secret returned to the browser")
	}
	// Saving the form back with blank secrets keeps the stored ones.
	var cfg config.Config
	_ = json.Unmarshal(raw, &cfg)
	cfg.ServerName = "Domowe Kino"
	if code, body, _ := h.call("PUT", "/api/config", cfg, true); code != 200 {
		t.Fatalf("resave: %d %v", code, body)
	}
	stored, _ := os.ReadFile(h.path)
	if !strings.Contains(string(stored), silotest.Key) || !strings.Contains(string(stored), "smtp-secret-value") || !strings.Contains(string(stored), "Domowe Kino") {
		t.Fatal("blank secret fields erased stored values")
	}
	if info, _ := os.Stat(h.path); info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions %v", info.Mode().Perm())
	}
}

func TestEnablingScheduleDoesNotFireAPastSlot(t *testing.T) {
	h := newHarness(t)
	h.configure()
	_, _, raw := h.call("GET", "/api/config", nil, true)
	var cfg config.Config
	_ = json.Unmarshal(raw, &cfg)
	now := time.Now().In(cfg.Location())
	cfg.ScheduleEnabled = true
	cfg.ScheduleDay = now.Weekday().String()
	cfg.ScheduleTime = now.Add(-time.Minute).Format("15:04")
	if now.Hour() == 0 && now.Minute() == 0 {
		t.Skip("midnight boundary")
	}
	if code, body, _ := h.call("PUT", "/api/config", cfg, true); code != 200 {
		t.Fatalf("save schedule: %d %v", code, body)
	}
	if last := h.server.Store.LastSlot(); time.Since(last) > 2*time.Minute {
		t.Fatalf("past slot was not claimed on enable: %v", last)
	}
}

func TestUsersPreviewAndSendAll(t *testing.T) {
	h := newHarness(t)
	h.configure()
	code, _, raw := h.call("GET", "/api/silo/users", nil, true)
	// Silo's own addresses never reach the browser; only admin-set overrides do.
	if code != 200 || strings.Contains(string(raw), "alice@example.com") || strings.Contains(string(raw), "bob@example.com") {
		t.Fatalf("users list: %d %s", code, raw)
	}
	code, preview, _ := h.call("POST", "/api/preview", map[string]string{"UserID": "1"}, true)
	if code != 200 || !strings.Contains(preview["html"].(string), "Cześć, alice!") {
		t.Fatalf("preview: %d", code)
	}
	if code, body, _ := h.call("POST", "/api/send-all", map[string]string{"Confirm": "nie"}, true); code != http.StatusBadRequest {
		t.Fatalf("unconfirmed send-all: %d %v", code, body)
	}
	if code, body, _ := h.call("POST", "/api/send-all", map[string]string{"Confirm": "WYŚLIJ"}, true); code != http.StatusAccepted {
		t.Fatalf("send-all: %d %v", code, body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.server.isSending() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	runs := h.server.Store.Runs()
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].Sent != 3 || runs[0].Skipped != 1 {
		t.Fatalf("run: %+v", runs)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	recipients := map[string]string{}
	for _, message := range h.sent {
		recipients[message.to] = message.raw
	}
	for _, address := range []string{"alice@example.com", "bob@example.com", "dave.fallback@example.com"} {
		if _, ok := recipients[address]; !ok {
			t.Fatalf("missing delivery to %s: %v", address, len(h.sent))
		}
	}
	if _, ok := recipients["carol@example.com"]; ok {
		t.Fatal("disabled account was mailed")
	}
	if strings.Contains(recipients["alice@example.com"], "Prywatny Seans Boba") || !strings.Contains(recipients["bob@example.com"], "To: <bob@example.com>") {
		t.Fatal("newsletters were not isolated per recipient")
	}
}

func TestLayoutIsSavedNormalizedAndValidated(t *testing.T) {
	h := newHarness(t)
	h.configure()
	_, current, _ := h.call("GET", "/api/config", nil, false)
	if current["DefaultLayout"] == nil || current["Layout"] == nil {
		t.Fatalf("config has no layout: %v", current)
	}
	cfg, _ := config.LoadFile(h.path)
	cfg.Layout.Sections = []config.Section{{ID: config.SectionShows, Enabled: true, Style: "grid", Columns: 4}}
	cfg.Layout.Theme = "light"
	if code, body, _ := h.call("PUT", "/api/config", cfg, true); code != 200 {
		t.Fatalf("save layout: %d %v", code, body)
	}
	saved, _ := config.LoadFile(h.path)
	if len(saved.Layout.Sections) != 4 || saved.Layout.Sections[0].ID != config.SectionShows || saved.Layout.Sections[0].Columns != 4 || saved.Layout.Theme != "light" {
		t.Fatalf("saved layout: %+v", saved.Layout)
	}
	cfg.Layout.AccentColor = "javascript:alert(1)"
	if code, _, _ := h.call("PUT", "/api/config", cfg, true); code != http.StatusBadRequest {
		t.Fatalf("bad accent color accepted: %d", code)
	}
}
