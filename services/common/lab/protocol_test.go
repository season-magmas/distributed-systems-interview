package lab

import (
	"testing"
	"time"
)

func TestCommandValidation(t *testing.T) {
	now := time.Now().UTC()
	c := Command{Version: Version, ID: "command-1", RunID: "run-1", Step: "fill", Device: Devices[0], JobID: "job-1", Attempt: 1, CreatedAt: now, DispatchDeadline: now.Add(DispatchTimeout), RecoveryTimeout: DispatchTimeout}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../escape", "x.y", "", "a/b", "a b"} {
		d := c
		d.JobID = bad
		if d.Validate() == nil {
			t.Errorf("accepted unsafe identity %q", bad)
		}
	}
	c.Attempt = 4
	if c.Validate() == nil {
		t.Fatal("accepted exhausted attempt")
	}
}
func TestRetrySafetyAndBackoff(t *testing.T) {
	for _, out := range []string{"failed", "hung"} {
		if CanRetry(Devices[0], 1, out) {
			t.Fatal("unsafe dispense retry")
		}
		if !CanRetry(Devices[1], 1, out) || !CanRetry(Devices[2], 2, out) {
			t.Fatal("safe retry refused")
		}
		if CanRetry(Devices[1], 3, out) {
			t.Fatal("retry budget exceeded")
		}
	}
	if CanRetry(Devices[1], 1, "running") || CanRetry("unknown", 1, "failed") {
		t.Fatal("ambiguous retry")
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got := Backoff(i); got != w {
			t.Errorf("delay %d: %s want %s", i, got, w)
		}
	}
}
func TestReportCannotRewritePhysicalTruth(t *testing.T) {
	start := time.Now().UTC()
	finish := start.Add(time.Second)
	c := Command{JobID: "job-1"}
	a := Report{Command: c, Revision: 1, Executions: []Execution{{ID: "exec-1", Outcome: "completed", StartedAt: start, FinishedAt: &finish}}}
	b := a
	b.Revision = 2
	if err := CheckReport(a, b); err != nil {
		t.Fatal(err)
	}
	b.Executions = nil
	if CheckReport(a, b) == nil {
		t.Fatal("erased execution")
	}
	b = a
	b.Revision = 2
	b.Executions = append([]Execution(nil), a.Executions...)
	b.Executions[0].Outcome = "running"
	if CheckReport(a, b) == nil {
		t.Fatal("regressed physical outcome")
	}
}
