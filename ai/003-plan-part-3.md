# Part 3: automated system testing and chaos injection

Status: implemented and validated on 2026-10-24. The contract below is the
planning record; actual storage choices, test evidence, elapsed time and remaining
limits are documented in `../NOTES.md` and `../docs/validation.md`.

## Agreed decisions

- Extend `scripts/chaos.sh` to kill and restart drivers as well as the executor.
- Create isolated test-owned resources and automatically clean them up, including
  on failure. Preserve diagnostics outside the resources being removed.
- Provide a routine suite plus an extended suite. Routine testing includes
  targeted cases and one fixed random seed; extended testing adds more seeds,
  combinations, and longer recovery scenarios.
- Stop after the first failure, collecting diagnostics and cleaning owned resources;
  remaining scenarios are reported as not run. Part 5 adds NATS and Postgres
  restarts with intact volumes to the extended suite.

These tests verify the contracts in parts 1 and 2. A failed scientific run can
be the correct result of a fault test; test success means truthful reporting and
the expected recovery behavior, not necessarily workflow completion.

## Entry points and organization

Proposed commands:

```sh
./scripts/system-test.sh --suite routine
./scripts/system-test.sh --suite extended
./scripts/system-test.sh --scenario incubator-hang
```

The shell entry point owns build/setup, Compose configuration, fault processes,
artifact collection, and cleanup. A Go system-test runner owns bounded polling,
HTTP/GraphQL requests, ledger parsing, object hashing, and assertions. Keep system
tests opt-in so ordinary `go test ./...` does not require Docker or inject faults.
Use a build tag or dedicated runner; finalize the layout during implementation.

Record a machine-readable suite result and a short human-readable summary.
Exit nonzero for failed assertions, infrastructure/setup failures, unmet fault
preconditions, or cleanup failures. Distinguish these categories in the result.
Never turn unavailable prerequisites or a missed injection into a passing test.

The extended suite includes routine coverage. Reuse built images within a suite;
each scenario receives fresh data volumes so prior ledgers or runs cannot mask a
failure. Run fault scenarios serially initially to avoid resource contention and
make diagnosis easier.

Bootstrap the harness lifecycle and named scenarios alongside part 4. Its initial
gate is isolation, cleanup, and the happy path; full fault coverage is the part 5
gate. Before part 6, run explicit HTTP/ledger-only stages and mark GraphQL coverage
pending. After GraphQL is implemented, the final suite includes GraphQL assertions
and must fail if that endpoint is unavailable. Do not silently auto-skip checks.

## Isolation and lifecycle

1. Verify Docker/Compose, Go, Bash, curl, and jq are available. Record versions,
   commit ID, dirty-worktree status, image identities, and suite configuration.
2. Create a unique project name and artifact directory. Produce a resolved test
   Compose configuration from the application configuration, replacing fixed
   host ports with loopback-bound dynamically allocated ports and assigning an
   available test-only subnet with consistent static service addresses.
3. Validate the resolved configuration before starting containers. Every mutable
   volume and network must belong to this project. Do not mount the developer's
   data volumes or use a shared fixed project name.
4. Export the same Compose project/file selection to the runner and every chaos
   subprocess. The script changes its working directory, so use absolute paths.
   Do not assume a project-name override alone resolves subnet and port conflicts.
5. Discover published ports after startup. Pass `EXEC`, `S3`, and `INSTRUMENTS`
   to the existing acceptance script; use the same endpoints in the runner.
6. Verify actual readiness, including workflow loading, consumer provisioning,
   instrument status, and usable object storage, with bounded waits.
7. Set all failure and hang percentages explicitly for each scenario. Configure
   fault settings before creating runs, and verify the instrument status reports
   the expected settings. Do not accidentally inherit the developer's `.env`.
8. Register cleanup before resource creation. On every exit, stop/join fault
   subprocesses, collect diagnostics, then remove only owned containers, networks,
   and volumes. Preserve logs and results in the host artifact directory.

Use project ownership labels and a recorded resource inventory before destructive
cleanup. Never run global Docker prune or an unscoped `docker compose down -v`.
If subnet allocation races or a port cannot be assigned, retry setup with a new
allocation or report infrastructure failure; do not modify unrelated networks.

INT/TERM should reach fault subprocesses so their restoration traps run. Allow a
bounded restoration period, then clean the owned stack even if restoration fails.
SIGKILL or host failure cannot run traps: leave an ownership manifest that allows
explicit cleanup of that test's leaked resources on a later invocation.

## Changes to chaos.sh

- Allow `kill` only for executor and the three named driver services. Resolve the
  selected service through the active Compose project, kill its container, and
  restart that same container while retaining its ledger/spool volumes.
- Preserve the existing `kill executor`, disconnect, storage-down, and random
  command interfaces. Update help and error messages for driver kills.
- Keep the current random fault selection and seed semantics for compatibility.
  Driver kills are targeted scenarios initially; extended combinations can invoke
  them explicitly without changing the existing random sequence.
- Make restoration tolerate a service already restarted by its restart policy.
  Verify readiness in the harness after restart, not merely Docker start success.
- Resolve the project's lab network explicitly, and reconnect using the address
  and alias from the resolved test configuration. Reject ambiguous networks.
- Test argument validation, unsupported services, partial failure, and INT/TERM
  cleanup. A fault command returning success is insufficient: the harness must
  observe that the intended fault actually occurred.

Driver death means death of the simulated instrument too, because they share a
process. Network disconnect leaves physical work running. Tests must distinguish
these two situations and their different ledger outcomes.

## Synchronization, deadlines, and reproducibility

Target faults at observable milestones: job physically running, command committed,
data captured, or upload pending. Public instrument status can identify physical
start. Application persistence boundaries require test instrumentation or component
tests; sleeps alone cannot guarantee hitting them.

For process-death tests, use a longer configured step duration to create a practical
injection window, then assert the ledger proves the job was interrupted. If the
job completed before the kill, classify the injection as missed, not passed.

Proposed defaults: 120 seconds for readiness after images are built, 180 seconds
for an ordinary fault scenario, and 300 seconds for longer-hang or combined-fault
scenarios. Give builds a separate explicit timeout. Derive individual scenario
deadlines from its workflow length, number of attempts, configured hangs, recovery
schedule, and fault duration; reject an insufficient configured budget up front.
Three 30-second hangs plus observation delays cannot fit a 30- or 45-second test.

Use bounded HTTP requests and monotonic elapsed-time deadlines. Temporary endpoint
failures are expected only during the declared outage/recovery interval; they must
not suppress final assertions. Record timestamps for observations and actual fault
start/end. Use tolerant timing assertions to check ordering and minimum backoff,
not exact millisecond equality across containers.

Record `CHAOS_SEED` independently from the chaos script's random `--seed`. Fixed
seeds reproduce fault selection but not process scheduling. Maintain small,
verified seed fixtures for fail-then-succeed and hang-then-succeed sequences; confirm
their actual ledger outcomes. Use 100% fault settings for exhaustion cases.

## Routine suite

| Case | Setup/injection | Required outcome |
| --- | --- | --- |
| Happy path | All faults zero; unchanged `acceptance.sh` | All seven checks pass |
| Executor restart | Kill during an observed active job | Same job settles, no duplicate physical attempt |
| Network partitions | Disconnect executor and each driver in separate cases | Reconcile after reconnect; physical work during a partition is represented |
| Driver death | Kill each driver during observed execution | Ledger restart outcome retained; liquid handler fails once, safe devices retry within budget |
| Storage outage | Stop storage during a plate read/upload | Capture bytes, retry upload, verify original measurement hash; no re-read |
| Retryable failure | Confirmed fail-then-succeed fixture and persistent failure on a safe device | Correct success after retry, or failure after exactly three executions |
| Unsafe failure | Liquid handler at 100% failure | One physical execution; no dependent work |
| Default hang | Incubator/reader hang-then-succeed fixtures with 30-second hangs | Observe release before retry; complete without device overlap |
| Unsafe hang | Liquid handler at 100% hang | One execution, eventually failed after known terminal hang |
| Concurrent runs | At least two workflows sharing devices | No overlapping physical intervals on the same device; distinct devices overlap |
| Cancellation | Before authorization, during physical work, during retry backoff | No new authorization after intent; actual dispatched outcomes retained |
| Random chaos | `chaos.sh random --seed 7 --for 60` with continuous workflow submission | Work overlaps the fault window; after submission stops and faults recover, all scenario runs settle within a backlog-aware deadline and satisfy record invariants |

Run a final fault-free acceptance case after the fault scenarios. Restore settings
explicitly even though scenarios use isolated data, and verify those settings.
Record actual runtime when implemented; do not promise the routine suite is fast
while retaining real 30-second hang coverage.

## Extended suite

Include routine coverage, then add:

- NATS and Postgres process restarts with intact volumes, as specified in part 5.

- Additional fixed random seeds (proposed 11, 23, 47) with longer fault windows.
- Longer configured hang, such as 60 seconds, with a matching recovery timeout;
  repeated hangs exhausting all three attempts and a restart during recovery.
- Disconnect exceeding the recovery deadline: observe `running` with a recovery
  error and dispatch paused, then reconnect and verify eventual reconciliation.
- Storage outage plus plate-reader death after durable capture, proving spool
  recovery, and cancellation during upload or an extended partition.
- Executor restart during retry backoff, command publication, and result handling.
- Cancellation after outbox commit but before delivery, followed by delayed command
  execution and truthful settlement, as allowed by parts 1 and 2.
- More concurrent workflows and delayed/reordered reports, including old reports
  arriving after a newer attempt has started.
- Capacity/backpressure, corrupt spool records, conflicting hashes, and permanently
  unavailable evidence, with expected explicit errors and safe dispatch pause.

Use finite bounded load for random tests: keep at least one run active during the
fault window, cap simultaneous runs, stop submitting when injection ends, and drain
all created runs. Record counts and budget enough time for queued work; an arbitrary
subset of completed runs cannot stand in for the scenario's entire ledger.

Use controlled component/integration tests for instruction-sized windows: loss of
bytes before durable capture, crash after broker acceptance before local update,
and crash after Postgres commit before result acknowledgement. Keep those tests
part of required validation even when process-level chaos cannot hit them reliably.
Do not modify `instrument.go` or claim a process test covered an unobserved window.

## Assertions and independent evidence

Read instrument `/status` on each device and filter by the scenario's run IDs.
Download objects directly and compute SHA-256. Compare with HTTP, and with GraphQL
once implemented. Internal database/spool inspection may synchronize a fault or
explain failure, but cannot substitute for independent physical/data assertions.

Required invariants across both suites:

- Every reported successful step has a corresponding successful physical execution.
- Every physical execution is accounted for, including failures and permitted
  retries. Duplicate physical executions for one job ID fail the test.
- Safe retries use distinct job IDs and remain within three attempts; unsafe jobs
  never repeat. Message redelivery and busy responses consume no physical attempts.
- No device has overlapping physical execution intervals, across all test runs.
- Dependency executions begin only after prerequisites have logically succeeded,
  including any required data persistence. Track public observations as well as
  ledger times to test data-dependent ordering. Polling alone cannot prove that
  every transient state was valid: use transaction/component tests and a persisted
  authorization trace for the data-readiness boundary, retaining the ledger as the
  independent physical-time oracle.
- Every published data reference names accessible bytes matching the selected
  execution's hash after storage recovery. No successful read silently loses data.
- Unknown work remains running with an explicit error, without speculative retries
  or new dependent authorization. Late evidence resolves the record truthfully.
- Cancellation preserves dispatched work; a terminal cancelled run has no remaining
  unaccounted authorized job or physical work.

Fault tests must not reuse the happy-path script's blanket prohibition on repeated
steps: retries are legitimate when their distinct attempts and safety policy agree.
Assert expected terminal outcomes per scenario; a stuck run must fail a scenario
that requires recovery, even if it has not made a false claim.

## Diagnostics, reporting, and completion criteria

Save per-scenario configuration, endpoints, seeds, run/job IDs, fault command logs,
fault timestamps, periodic API/ledger snapshots, object hashes, service logs,
read-only spool/database diagnostics where needed, and assertion results. Collect
bounded diagnostics even after test timeout or setup failure. Do not retain secrets
from unrelated host environment variables in artifacts.

Emit a summary with pass/fail/error counts, elapsed time, cleanup status, and
artifact paths; optionally emit JUnit for CI. Artifacts should be ignored by Git
by default, with selected evidence summarized in `NOTES.md`.

The harness is complete when it can demonstrate a failing invariant against an
intentionally broken test fixture, pass the implemented recovery scenarios, and
clean its resources on success, failure, and interruption. Before features exist,
unmet scenarios are explicit failures or documented pending coverage, never passes.
The routine suite must exercise both provided workflows: run the seven-check
acceptance script for the default workflow and `Triple Assay`, with faults zero.
The second workflow includes a data-producing dependency and multiple reads.

Part 4 specifies the first implementation slice and its acceptance gate.
