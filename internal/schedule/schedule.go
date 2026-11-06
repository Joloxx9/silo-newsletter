// Package schedule sends the newsletter once a week at the configured time.
package schedule

import (
	"context"
	"log/slog"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
)

// Grace is how late a missed slot may still be sent, e.g. after a restart.
const Grace = 6 * time.Hour

// Slot returns the most recent scheduled time at or before now.
func Slot(now time.Time, cfg config.Config) (time.Time, bool) {
	if cfg.ValidateSchedule() != nil {
		return time.Time{}, false
	}
	location := cfg.Location()
	local := now.In(location)
	clock, _ := time.Parse("15:04", cfg.ScheduleTime)
	target := 0
	for index, day := range config.Weekdays {
		if day == cfg.ScheduleDay {
			target = (index + 1) % 7 // time.Weekday: Sunday == 0
		}
	}
	back := (int(local.Weekday()) - target + 7) % 7
	day := local.AddDate(0, 0, -back)
	slot := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
	if slot.After(local) {
		slot = slot.AddDate(0, 0, -7)
	}
	return slot, true
}

// Next returns the next scheduled time after now.
func Next(now time.Time, cfg config.Config) (time.Time, bool) {
	slot, ok := Slot(now, cfg)
	if !ok {
		return time.Time{}, false
	}
	next := slot.AddDate(0, 0, 7)
	// AddDate keeps the wall clock across DST changes.
	return next, true
}

// Due reports whether slot should be sent now.
func Due(now, slot, lastClaimed time.Time) bool {
	return !slot.IsZero() && !now.Before(slot) && now.Sub(slot) <= Grace && slot.After(lastClaimed)
}

type Runner struct {
	LoadConfig func() (config.Config, error)
	LastSlot   func() time.Time
	ClaimSlot  func(time.Time) (bool, error)
	SendAll    func(context.Context, config.Config) error
	Log        *slog.Logger
	Every      time.Duration
	Now        func() time.Time
}

// Run checks the schedule until ctx ends.
func (r Runner) Run(ctx context.Context) {
	every := r.Every
	if every == 0 {
		every = 30 * time.Second
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r Runner) Tick(ctx context.Context) {
	cfg, err := r.LoadConfig()
	if err != nil || !cfg.ScheduleEnabled {
		return
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	slot, ok := Slot(now, cfg)
	if !ok || !Due(now, slot, r.LastSlot()) {
		return
	}
	// Claim first: a crash mid-run must not resend to people already mailed.
	claimed, err := r.ClaimSlot(slot)
	if err != nil || !claimed {
		if err != nil {
			r.Log.Error("could not record the schedule slot; not sending", "error", err)
		}
		return
	}
	r.Log.Info("scheduled newsletter starting", "slot", slot.Format(time.RFC3339))
	if err := r.SendAll(ctx, cfg); err != nil {
		r.Log.Error("scheduled newsletter failed", "error", err)
	}
}
