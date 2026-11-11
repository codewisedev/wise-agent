package improvement

import (
	"encoding/json"
	"strings"
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

func TestFitTrimsToBudget(t *testing.T) {
	big := func(n int) string { return strings.Repeat("x", n) }
	d := Digest{WindowDays: 30, Rules: map[string]any{"max_proposals": 5}}
	for i := 0; i < 20; i++ {
		d.Agents = append(d.Agents, map[string]any{"agent": "a", "enough_runs": i < 3, "rejected": int64(0), "failed": int64(0), "thumbs_down": int64(0)})
		d.Failures = append(d.Failures, map[string]any{"message": big(200)})
		d.Memories = append(d.Memories, map[string]any{"body": big(300)})
	}
	for i := 0; i < 4; i++ {
		d.Prompts = append(d.Prompts, map[string]any{"agent": "p", "prompt": big(8000)})
	}
	d.Templates = []map[string]any{{"id": "t", "name": "T", "definition": json.RawMessage(`{"steps":[{"id":"a","agent":"x","review":true},{"id":"g","type":"gate"}]}`)}}
	Fit(&d, 9000)
	if n := len(mustJSON(&d)); n > 9000 {
		t.Fatalf("still %d chars", n)
	}
	if len(d.Trimmed) == 0 || len(d.Agents) != 3 {
		t.Fatalf("trimmed %v, agents %d", d.Trimmed, len(d.Agents))
	}
	small := Digest{WindowDays: 30}
	Fit(&small, 9000)
	if small.Trimmed != nil {
		t.Fatal("trimmed a digest that fits")
	}
}
