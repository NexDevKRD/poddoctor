package severity

import "testing"

func TestOf(t *testing.T) {
	cases := map[string]string{
		"OOMKilled":               "critical",
		"SegFault":                "critical",
		"ImagePullError":          "high",
		"BadCommand":              "high",
		"ProbeFailure":            "high",
		"SignalKilled":            "medium",
		"RecentRolloutRegression": "medium",
		"ApplicationError":        "medium",
		"Unknown":                 "unknown",
		"":                        "unknown",
	}
	for cause, want := range cases {
		if got := Of(cause); got != want {
			t.Errorf("Of(%q) = %q, want %q", cause, got, want)
		}
	}
}
