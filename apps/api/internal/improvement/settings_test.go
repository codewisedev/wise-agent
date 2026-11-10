package improvement

import (
	"testing"
	"time"
)

func TestScheduleDue(t *testing.T) {
	st := Settings{Schedule: Schedule{Enabled: true, Weekday: "monday", Hour: 7}}
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	cases := map[string]string{
		"2026-11-09T06:59:00Z": "",         // Monday before the hour: last week's moment is over a day old
		"2026-11-09T07:00:00Z": "2026-W46", // due
		"2026-11-09T20:00:00Z": "2026-W46", // same period all day (idempotent by period)
		"2026-11-10T06:59:00Z": "2026-W46", // within a day of catching up
		"2026-11-10T07:01:00Z": "",         // too late: wait for next week
	}
	for now, want := range cases {
		if got := st.Due(at(now)); got != want {
			t.Errorf("%s: got %q want %q", now, got, want)
		}
	}
	st.Schedule.Enabled = false
	if st.Due(at("2026-11-09T07:00:00Z")) != "" {
		t.Error("disabled schedule due")
	}
	if _, err := ParseSettings([]byte(`{"schedule":{"enabled":true,"weekday":"funday","hour":7},"window_days":30,"min_runs":3,"max_proposals":5}`)); err == nil {
		t.Error("bad weekday accepted")
	}
	if _, err := ParseSettings([]byte(`{"schedule":{"enabled":true,"weekday":"Friday","hour":7},"window_days":30,"min_runs":3,"max_proposals":5,"extra":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
}
