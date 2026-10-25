package lab

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"time"
)

const (
	Version         = 1
	MaxAttempts     = 3
	CommandStream   = "LAB_COMMANDS"
	ResultStream    = "LAB_RESULTS"
	CommandPrefix   = "lab.commands."
	ResultPrefix    = "lab.results."
	PollInterval    = 200 * time.Millisecond
	DispatchTimeout = 45 * time.Second
	AckWait         = 30 * time.Second
	Heartbeat       = 10 * time.Second
	Stored          = "stored"
	Pending         = "pending"
	Lost            = "lost"
	NotApplicable   = "not_applicable"

	LiquidHandlerID  = "liquid-handler-1"
	IncubatorID      = "incubator-1"
	PlateReaderID    = "plate-reader-1"
	ExecutorConsumer = "executor"

	OutcomeRunning      = "running"
	OutcomeCompleted    = "completed"
	OutcomeFailed       = "failed"
	OutcomeHung         = "hung"
	MeasurementDataLost = "measurement_data_lost"
)

var Devices = []string{LiquidHandlerID, IncubatorID, PlateReaderID}

type Command struct {
	Version          int           `json:"version"`
	ID               string        `json:"id"`
	RunID            string        `json:"run_id"`
	Step             string        `json:"step"`
	Device           string        `json:"device"`
	JobID            string        `json:"job_id"`
	Attempt          int           `json:"attempt"`
	CreatedAt        time.Time     `json:"created_at"`
	DispatchDeadline time.Time     `json:"dispatch_deadline"`
	RecoveryTimeout  time.Duration `json:"recovery_timeout"`
}
type Execution struct {
	ID         string     `json:"id"`
	Outcome    string     `json:"outcome"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      string     `json:"error"`
	Retryable  bool       `json:"retryable"`
	Hash       string     `json:"hash"`
	DataState  string     `json:"data_state"`
	DataRef    string     `json:"data_ref"`
	DataError  string     `json:"data_error"`
}
type Report struct {
	Version          int         `json:"version"`
	ID               string      `json:"id"`
	Command          Command     `json:"command"`
	Revision         int         `json:"revision"`
	Executions       []Execution `json:"executions"`
	RecoveryDeadline *time.Time  `json:"recovery_deadline"`
	Error            string      `json:"error"`
	Terminal         bool        `json:"terminal"`
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func SafeID(s string) bool { return idPattern.MatchString(s) }
func Supported(device string) bool {
	for _, d := range Devices {
		if d == device {
			return true
		}
	}
	return false
}
func (c Command) Validate() error {
	if c.Version != Version || !Supported(c.Device) || c.Attempt < 1 || c.Attempt > MaxAttempts {
		return errors.New("invalid command version, device or attempt")
	}
	for _, s := range []string{c.ID, c.RunID, c.Step, c.JobID} {
		if !SafeID(s) {
			return errors.New("invalid command identity")
		}
	}
	if c.CreatedAt.IsZero() || !c.DispatchDeadline.After(c.CreatedAt) || c.RecoveryTimeout < DispatchTimeout {
		return errors.New("invalid recovery deadlines")
	}
	return nil
}
func Backoff(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n > 4 {
		n = 4
	}
	return (500 * time.Millisecond) * time.Duration(1<<n)
}
func CanRetry(device string, attempt int, outcome string) bool {
	return (device == IncubatorID || device == PlateReaderID) && attempt < MaxAttempts && attempt > 0 && (outcome == OutcomeFailed || outcome == OutcomeHung)
}
func CheckReport(old, next Report) error {
	if old.Command.JobID != "" && !reflect.DeepEqual(old.Command, next.Command) {
		return errors.New("job identity conflict")
	}
	if next.Revision <= old.Revision {
		if reflect.DeepEqual(old, next) {
			return nil
		}
		return errors.New("report revision conflict")
	}
	for _, a := range old.Executions {
		var b *Execution
		for i := range next.Executions {
			if next.Executions[i].ID == a.ID {
				b = &next.Executions[i]
				break
			}
		}
		if b == nil || !b.StartedAt.Equal(a.StartedAt) || (a.Outcome != OutcomeRunning && b.Outcome != a.Outcome) || (a.Hash != "" && a.Hash != b.Hash) || (a.FinishedAt != nil && (b.FinishedAt == nil || !a.FinishedAt.Equal(*b.FinishedAt))) || (a.DataState == Stored && (b.DataState != Stored || b.DataRef != a.DataRef)) {
			return fmt.Errorf("physical evidence conflict: %s", a.ID)
		}
	}
	if old.Terminal && !next.Terminal {
		return errors.New("terminal report regression")
	}
	return nil
}
