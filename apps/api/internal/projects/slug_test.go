package projects

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Salon Booking Platform": "salon-booking-platform",
		"  Wise -- AI!! ":        "wise-ai",
		"Ünïcode Name 2":         "n-code-name-2",
		"!!!":                    "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
