package main

import (
	"testing"
	"time"
)

func TestSettlePreservesActiveAndFailedWork(t *testing.T) {
	cases := []struct {
		name              string
		cancel            bool
		status            string
		job               string
		retry             bool
		wantRun, wantStep string
	}{
		{"active cancellation", true, StepRunning, "job-1", false, RunRunning, StepRunning},
		{"retry cancellation", true, StepRunning, "", true, RunCancelled, StepFailed},
		{"pending cancellation", true, StepPending, "", false, RunCancelled, StepCancelled},
		{"completed cancellation", true, StepCompleted, "", false, RunCancelled, StepCompleted},
		{"success", false, StepCompleted, "", false, RunCompleted, StepCompleted},
		{"failure", false, StepFailed, "", false, RunFailed, StepFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &stepRuntime{Job: tc.job}
			if tc.retry {
				due := time.Now().Add(time.Second)
				st.RetryAt = &due
			}
			r := runRecord{View: RunView{Status: RunRunning, Steps: []StepView{{Name: "step", Status: tc.status}}}, Cancel: tc.cancel, State: map[string]*stepRuntime{"step": st}}
			settle(&r)
			if r.View.Status != tc.wantRun || r.View.Steps[0].Status != tc.wantStep {
				t.Fatalf("wrong settlement: %+v", r)
			}
			if tc.wantRun == RunRunning && r.View.FinishedAt != nil {
				t.Fatal("invented finish time")
			}
		})
	}
}
func TestNextStepsDependencyAndDeviceExclusion(t *testing.T) {
	steps := []StepState{{Name: "fill", DeviceID: "liquid-handler-1", Status: StepCompleted}, {Name: "a", DeviceID: "incubator-1", DependsOn: []string{"fill"}, Status: StepPending}, {Name: "b", DeviceID: "incubator-1", Status: StepPending}, {Name: "read", DeviceID: "plate-reader-1", DependsOn: []string{"a"}, Status: StepPending}}
	next := NextSteps(steps)
	if len(next) != 1 || next[0] != "a" {
		t.Fatalf("unsafe scheduling: %v", next)
	}
	steps[1].Status = StepRunning
	next = NextSteps(steps)
	if len(next) != 0 {
		t.Fatal("busy device or dependency bypassed")
	}
}
