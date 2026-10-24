package lab

import (
	"errors"
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
)

var Devices = []string{"liquid-handler-1", "incubator-1", "plate-reader-1"}

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

func SafeID(s string) bool                                     { return false }
func (c Command) Validate() error                              { return errors.New("not implemented") }
func Backoff(n int) time.Duration                              { return 0 }
func CanRetry(device string, attempt int, outcome string) bool { return false }
func CheckReport(old, next Report) error                       { return errors.New("not implemented") }
