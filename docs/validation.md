# Validation evidence

The suite is opt-in because it builds and controls Docker services. It creates a
unique Compose project, dedicated subnet, stable isolated host ports and fresh
volumes for each scenario, retains artifacts under `artifacts/`, and removes its
containers/networks/volumes on exit. It stops after its first failure. Read
`results.json` for completed, failed and not-run scenarios; a missing scenario is
not a passing check.

## Reproduction

```sh
go test ./...
go test -cover ./services/...
python3 -m unittest discover -s scripts/tests
./scripts/system-test.sh --scenario integration
./scripts/system-test.sh --suite routine
./scripts/system-test.sh --suite extended
```

`--http-only` explicitly selects the intermediate pre-GraphQL stage. Final checks
use the default, which requires GraphQL and compares attempt history with the
independent instrument ledgers. `--scenario NAME` reruns a targeted scenario.
`LAB_TEST_HOST` overrides the address used for Docker's published ports; the runner
supports both native localhost and this workspace's Docker Desktop host gateway.
Requirements: Docker/Compose, Bash, Python 3, Go, curl, jq. Build and request waits
have explicit timeouts; real 30-second and extended 60-second hang cases take time.

To run Go race checks on a host with a C compiler:

```sh
CGO_ENABLED=1 go test -race ./...
```

In this workspace cgo is disabled and no C compiler is installed. Race checks were
also run inside the official Go 1.26 Alpine image with `build-base` installed,
copying `go.mod`, `go.sum`, `services/`, and `config/` into the test container.
No application volumes were mounted into that container.

Real Postgres/NATS tests run automatically in the integration scenario before
application containers start. For an already isolated disposable infrastructure
stack, they can also be selected with `LAB_TEST_DATABASE` and `LAB_TEST_NATS`.
The transport integration test purges its command stream; never point these
variables at a development stack containing work you want to retain.

## Evidence collected during implementation

- The initial default-workflow acceptance run passed all seven checks, including
  independent device overlap and downloaded data matching the instrument SHA-256.
- GraphQL create/start/poll/complete passed on an isolated stack, with its physical
  execution IDs checked against each instrument's independent `/status` ledger.
- A separate real-service integration run passed command redelivery, truthful
  cancellation, duplicate result handling, retry-backoff cancellation, rejection
  of unverified measurement success, GraphQL operations, and injected outbox
  failure rolling back the whole authorization transaction.
- Unit/component checks cover identity validation, safe retry bounds, report
  non-regression, exclusive spool ownership, corruption/capacity, capture recovery,
  known measurement loss, hang observation, hash conflicts, object round trips,
  workflow DAG errors, HTTP error contracts, and GraphQL validation.

Full-suite results and exact run identifiers are recorded in NOTES.md after the
fault campaign completes. Raw artifacts are ignored by Git; preserve or export the
referenced run directory if independent inspection of every snapshot is required.

## Failures that changed the implementation

1. Postgres socket readiness initially detected its temporary initialization
   server. The harness now probes TCP readiness before loading the schema.
2. Docker can reassign an unspecified published port on container restart. An
   executor-kill test physically completed but the runner polled the old address.
   The runner now assigns stable isolated ports before container creation.
3. An integration test showed a stored-data claim could complete a read without
   its hash/reference. The executor now rejects that terminal report durably.
4. Recovery timeout initially failed to grow with configured long work/hangs.
   The driver now uses at least `max(normal duration, hang duration) + 15s`, keeps
   physical and dispatch deadlines separate, and uses capped exponential polling
   while waking immediately for a normal outcome.
5. A verified existing object unnecessarily depended on another successful PUT.
   The driver now derives its reference after GET/hash verification without
   rewriting it. The regression test also caught a terminal stored-data report
   with an empty reference on the failed repeat-PUT path.
6. Unknown workflow errors in GraphQL initially returned an unhelpful generic
   message. HTTP and GraphQL now share workflow creation and preserve a useful
   domain error while hiding internal failures.

7. Late terminal evidence settled a run but retained its old timeout pause flag.
   A real Postgres regression now requires resolved terminal runs to clear it.
8. A spool containing physical evidence could be overwritten when the ledger was
   missing that execution. The adapter now preserves the spool and reports a
   conflict without executing replacement work.
9. Oracle GETs were initially retried only during progress polling. The runner
   now tolerates bounded read outages in final evidence collection and random-fault
   monitoring, while never automatically replaying a mutation request.

## Test boundaries

Process faults are synchronized with observed active executions. Killed-driver
cases require the targeted ledger execution to become hung, otherwise the fault
missed its intended window and the case fails. Random tests use finite concurrent
load and drain all created runs after injection stops. Both supplied workflows
run through the unchanged acceptance script.

Precise instruction-level crash windows are exercised with focused tests and
controlled dependency failures, not inferred from random seeds. The tests cannot
establish power-loss tolerance, broker/database volume-loss recovery, safe multiple
active owners, or lossless capture across the instrument-to-spool handoff.
