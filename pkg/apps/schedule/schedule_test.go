package schedule

import (
	"testing"
	"time"
)

func TestCronNext(t *testing.T) {
	at := time.Date(2026, 11, 11, 7, 58, 30, 0, time.UTC) // a Wednesday
	for expr, want := range map[string]string{
		"* * * * *":        "2026-11-11T07:59:00Z",
		"*/15 * * * *":     "2026-11-11T08:00:00Z",
		"0 9 * * 1-5":      "2026-11-11T09:00:00Z",
		"30 6 * * 0,6":     "2026-11-14T06:30:00Z",
		"0 0 1 1 *":        "2027-01-01T00:00:00Z",
		"5-10/5 12 29 2 *": "2028-02-29T12:05:00Z",
		"0 0 15 * 1":       "2026-11-15T00:00:00Z", // both days given: the 15th, a Sunday, or a Monday
		"0 0 20 * 1":       "2026-11-16T00:00:00Z", // the Monday comes first
		"0 0 20,25 12 2-3": "2026-12-01T00:00:00Z", // a Tuesday; the month still has to match
		"0 0 */2 * 1":      "2026-11-23T00:00:00Z", // a day-of-month that starts with * restricts: an odd Monday
		"0 0 15 * */2":     "2026-11-15T00:00:00Z", // and so does such a day-of-week: the 15th is a Sunday
		"0 0 16 * */2":     "2027-01-16T00:00:00Z", // the first 16th on an even day of the week, a Saturday
	} {
		got, err := next(expr, at)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("%s: %s %v, want %s", expr, got.Format(time.RFC3339), err, want)
		}
	}
	for _, expr := range []string{"* * * *", "60 * * * *", "* * 0 * *", "a * * * *", "*/0 * * * *", "0 0 31 2 *"} {
		if got, err := next(expr, at); err == nil {
			t.Errorf("%s: %s, want an error", expr, got)
		}
	}
}
