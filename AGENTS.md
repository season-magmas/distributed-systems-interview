# Project guidance

## Purpose and scope

Read `README.md` before changing behavior. This exercise builds a lab workflow
executor and instrument drivers. The central requirement is a truthful run
record: API statuses and plate-reader data must agree with physical execution,
including during faults. The work includes robust NATS communication, a GraphQL
interface at `/graphql`, and an evidence-based `NOTES.md`.

## Repository map

- `services/executor/`: run persistence, scheduling, HTTP APIs, and the future
  GraphQL interface. `scheduler.go` provides the pure `NextSteps` function.
- `services/driver/adapter.go`: connects an instrument to NATS and object storage.
- `services/driver/instrument/instrument.go`: fixed instrument implementation;
  read it to understand behavior, but never modify it.
- `services/common/storage/`: shared S3 client.
- `db/init.sql`: initial Postgres schema; changes are allowed.
- `config/workflows.yaml`: workflow DAG definitions.
- `examples/`: standalone NATS examples, not system components.
- `scripts/acceptance.sh` and `scripts/chaos.sh`: happy-path verification and
  fault injection.

Only the executor uses Postgres. The executor and drivers can reach NATS and
object storage. Each physical device executes one job at a time.

## Compatibility requirements

- Preserve `Executor` and the signatures of `NewExecutor`, `Start`, `Close`,
  `CreateRun`, `StartRun`, `CancelRun`, `GetRun`, and `ListRuns`.
- Preserve `Serve(ctx, inst, nc, st)` in the driver adapter.
- Preserve `RunView`, `StepView`, their JSON names, and the status values
  `pending`, `running`, `completed`, `failed`, and `cancelled`.
- Preserve the HTTP API paths and response contracts in `api.go`; acceptance
  checks use this API. Serve GraphQL on the executor's existing port.
- Step `data_ref` values identify stored data as `s3://bucket/key`.

## Go conventions

- Follow idiomatic Go and the Go version declared in `go.mod`. Format changed
  Go files with `gofmt`.
- All new or changed behavior must have good test coverage. Cover meaningful
  success, failure, boundary, and recovery cases; do not rely only on coverage
  percentages or tests that repeat implementation details.
- Include comments only when they explain something the code does not make
  obvious, such as an invariant, a safety constraint, or a design decision.
- Avoid magic values. Use descriptive named constants or variables for
  non-obvious function arguments, durations, limits, and protocol values.
- Prefer constants for reused strings, including commands, subjects, and status
  values. Reuse existing constants where possible.
- Optimize for reading and review: prefer more small, simple functions with
  clear responsibilities over long functions or clever abstractions.
- Handle errors explicitly and add useful context when wrapping them. Make
  cancellation, goroutine ownership, and resource cleanup clear.

## Reliability reasoning

- An instrument context ending stops the wait, not the physical work. A timeout
  or lost reply alone is not proof that a job did not execute.
- Account for duplicate delivery, reconnects, process restarts, busy devices,
  hangs, and storage failures when designing state transitions and retries.
- Use the instrument ledger and stable job identities to reason about physical
  execution. Respect the instrument's retryability result; a partially dispensed
  liquid-handler job cannot safely be repeated.
- Document message subjects, streams, consumers, acknowledgement timing, state
  ownership, and any remaining uncertainty in `NOTES.md` as they are implemented.

## Validation and working notes

- Run appropriate unit tests for behavior changes; use `go test ./...` for the
  full suite and `go test -cover ./...` to inspect coverage. Use race detection
  where concurrency changes warrant it.
- Start the stack with `docker compose up --build`. Rebuild changed services
  using the commands in the README, then run `./scripts/acceptance.sh`.
- Acceptance is only the floor. Exercise relevant faults with
  `./scripts/chaos.sh` and instrument failure/hang settings; record exact
  commands, seeds, observed outcomes, and resulting fixes.
- Restore instrument fault percentages to zero before acceptance testing.
- Recreating the database after schema changes with `docker compose down -v`
  deletes volumes; only do so when authorized to discard their data.
- Keep `NOTES.md` current. Distinguish observed results from assumptions and
  untested claims. Record actual time spent and feedback on the exercise.
- Include AI conversations in `ai/` or link them from `NOTES.md` before
  submission, and disclose omissions as requested by the README.
- Do not claim checks passed unless they ran. For documentation-only changes,
  review consistency and use `git diff --check`; running the stack is unnecessary.
