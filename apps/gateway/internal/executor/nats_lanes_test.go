package executor

import "testing"

// TestLaneFromPriorityContractValues pins the dispatch-lane mapping for every
// contract priority value (job-request.schema.json: low|normal|high|urgent).
// A schema-legal priority must never silently fall into the default lane.
func TestLaneFromPriorityContractValues(t *testing.T) {
	cases := []struct {
		priority string
		want     string
	}{
		{"low", "low"},
		{"normal", "norm"},
		{"high", "high"},
		{"urgent", "crit"},
		{"", "norm"},
		{"URGENT", "crit"},   // case-insensitive like every other value
		{" urgent ", "crit"}, // trimmed like every other value
	}
	for _, tc := range cases {
		if got := laneFromPriority(tc.priority); got != tc.want {
			t.Errorf("laneFromPriority(%q) = %q; want %q", tc.priority, got, tc.want)
		}
	}
}
