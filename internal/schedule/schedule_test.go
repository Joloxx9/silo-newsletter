package schedule

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/joloxx9/silo-newsletter/internal/config"
)

func cfgAt(day, clock string) config.Config {
	cfg := config.Defaults()
	cfg.ScheduleEnabled = true
	cfg.ScheduleDay = day
	cfg.ScheduleTime = clock
	return cfg
}

func TestSlotAndNext(t *testing.T) {
	warsaw, _ := time.LoadLocation("Europe/Warsaw")
	cfg := cfgAt("Friday", "09:30")
	// Friday 2026-11-06 10:00 Warsaw: today's slot already passed.
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, warsaw)
	slot, _ := Slot(now, cfg)
	if want := time.Date(2026, 11, 6, 9, 30, 0, 0, warsaw); !slot.Equal(want) {
		t.Fatalf("slot %v, want %v", slot, want)
	}
	next, _ := Next(now, cfg)
	if want := time.Date(2026, 11, 13, 9, 30, 0, 0, warsaw); !next.Equal(want) {
		t.Fatalf("next %v, want %v", next, want)
	}
	// Friday 09:00: the last slot was a week earlier.
	slot, _ = Slot(time.Date(2026, 11, 6, 9, 0, 0, 0, warsaw), cfg)
	if want := time.Date(2026, 10, 30, 9, 30, 0, 0, warsaw); !slot.Equal(want) {
		t.Fatalf("early slot %v, want %v", slot, want)
	}
	// Sunday schedule computed from a Monday.
	slot, _ = Slot(time.Date(2026, 11, 9, 12, 0, 0, 0, warsaw), cfgAt("Sunday", "18:00"))
	if want := time.Date(2026, 11, 8, 18, 0, 0, 0, warsaw); !slot.Equal(want) {
		t.Fatalf("sunday slot %v, want %v", slot, want)
	}
}

func TestDueHonoursGraceAndClaims(t *testing.T) {
	slot := time.Date(2026, 11, 6, 8, 30, 0, 0, time.UTC)
	if !Due(slot.Add(time.Minute), slot, time.Time{}) {
		t.Fatal("fresh slot not due")
	}
	if Due(slot.Add(Grace+time.Minute), slot, time.Time{}) {
		t.Fatal("slot past grace was due")
	}
	if Due(slot.Add(time.Minute), slot, slot) {
		t.Fatal("claimed slot was due again")
	}
}

func TestTickSendsOncePerSlot(t *testing.T) {
	warsaw, _ := time.LoadLocation("Europe/Warsaw")
	now := time.Date(2026, 11, 6, 9, 31, 0, 0, warsaw)
	var last time.Time
	sends := 0
	runner := Runner{
		LoadConfig: func() (config.Config, error) { return cfgAt("Friday", "09:30"), nil },
		LastSlot:   func() time.Time { return last },
		ClaimSlot: func(slot time.Time) (bool, error) {
			if !slot.After(last) {
				return false, nil
			}
			last = slot
			return true, nil
		},
		SendAll: func(context.Context, config.Config) error { sends++; return nil },
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:     func() time.Time { return now },
	}
	runner.Tick(context.Background())
	runner.Tick(context.Background())
	now = now.Add(time.Hour)
	runner.Tick(context.Background())
	if sends != 1 {
		t.Fatalf("sent %d times, want 1", sends)
	}
	disabled := runner
	disabled.LoadConfig = func() (config.Config, error) {
		cfg := cfgAt("Friday", "09:30")
		cfg.ScheduleEnabled = false
		return cfg, nil
	}
	last = time.Time{}
	disabled.Tick(context.Background())
	if sends != 1 {
		t.Fatal("disabled schedule sent")
	}
}
