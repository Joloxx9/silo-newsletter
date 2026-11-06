package newsletter

import (
	"fmt"
	"time"
)

// Plural picks the Polish form for n: one (1), few (2-4, not 12-14), many.
func Plural(n int, one, few, many string) string {
	if n == 1 {
		return one
	}
	lastTwo := n % 100
	last := n % 10
	if last >= 2 && last <= 4 && (lastTwo < 12 || lastTwo > 14) {
		return few
	}
	return many
}

var monthsGenitive = [...]string{"stycznia", "lutego", "marca", "kwietnia", "maja", "czerwca", "lipca", "sierpnia", "września", "października", "listopada", "grudnia"}

// FormatPeriod renders [from, to) as "30 października – 5 listopada 2026".
func FormatPeriod(from, to time.Time) string {
	last := to.AddDate(0, 0, -1)
	if from.Year() == last.Year() {
		if from.Month() == last.Month() {
			return fmt.Sprintf("%d – %d %s %d", from.Day(), last.Day(), monthsGenitive[last.Month()-1], last.Year())
		}
		return fmt.Sprintf("%d %s – %d %s %d", from.Day(), monthsGenitive[from.Month()-1], last.Day(), monthsGenitive[last.Month()-1], last.Year())
	}
	return fmt.Sprintf("%d %s %d – %d %s %d", from.Day(), monthsGenitive[from.Month()-1], from.Year(), last.Day(), monthsGenitive[last.Month()-1], last.Year())
}

// FormatMinutes renders a runtime such as "2 godz. 10 min".
func FormatMinutes(minutes int) string {
	hours, rest := minutes/60, minutes%60
	switch {
	case hours == 0:
		return fmt.Sprintf("%d min", rest)
	case rest == 0:
		return fmt.Sprintf("%d godz.", hours)
	default:
		return fmt.Sprintf("%d godz. %d min", hours, rest)
	}
}

// FormatDuration renders watch time from seconds.
func FormatDuration(seconds float64) string {
	return FormatMinutes(int(seconds/60 + 0.5))
}
