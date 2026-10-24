# Part 2: messaging, persistence, and restart recovery

Status: detailed specification for implementation planning, 2026-10-24.
No implementation or system testing has occurred for this specification.
Read alongside `003-plan-part-1.md` and `002-plan-response.md`.

## Agreed choices and scope

- JetStream carries durable commands and results; a Postgres outbox bridges
  executor transactions and command publication.
- Deploy one executor and one adapter per device, supporting concurrent runs.
  Multiple active replicas and leader election are outside the initial scope.
- Each driver has a durable local spool for captured measurement bytes, job
  state, and pending results. It survives process/container restarts.
- Unresolved work stays `running`, with an explicit recovery error and further
  dispatch paused for the affected run/device. Late evidence can resolve it.
- Preserve the physical-attempt, cancellation, and data-loss rules from part 1.

The concrete contracts and defaults below are design proposals implementing those
choices. They do not imply that the user selected every configuration detail.

## Ownership and topology

```text
HTTP / GraphQL
      |
executor -> Postgres: runs, steps, attempts, device reservations, outbox, inbox
      |                                    |
      |                              outbox publisher
      |                                    |
      |                     JetStream: lab.commands.<device-id>
      |                                    |
      |                        one serial adapter per device
      |                           |                  |
      |                     instrument ledger   durable spool
      |                                              |
      |                                     verified S3 object
      |                                              |
      +-------- JetStream: lab.results.<device-id> <--+
```

Postgres owns workflow intent and the public run record. The instrument owns
physical evidence. The spool owns received bytes and replayable driver reports.
Object storage owns published measurement objects. JetStream transports durable
messages; delivery count and broker deduplication never define execution count.

Only the executor connects to Postgres. Runtime reconciliation uses the driver's
local `inst.Status()` and reports over NATS. Instrument HTTP status remains the
independent test oracle, not an alternate executor control path.

## Message contracts

Use a versioned JSON envelope. Validate lengths, identifiers, version, and device
association before acting. Treat payload values as data, never filesystem paths.

| Message | Fields and semantics |
| --- | --- |
| Execute command | Schema version, command ID, run ID, step name, device ID, job ID, attempt number, creation time, dispatch deadline, configured physical recovery timeout |
| Driver report | Schema version, report ID, job identity, device ID, monotonic job revision, observed time, physical execution evidence, data state, recovery error |

Physical evidence includes execution ID, outcome, physical timestamps, error,
retryability where known, and measurement hash. Data state distinguishes not
applicable, pending, stored, and irrecoverably lost; stored includes `s3://` ref
and verified hash. Unknown evidence stays absent rather than becoming success.

A report is a cumulative snapshot for its job, not a delta requiring every prior
message. Publish after physical start, terminal outcome, data-state changes, and
physical recovery-deadline expiry. Retain all matching executions if an unexpected
duplicate is discovered and flag the violation. Never hide one by selecting a convenient row.

Persist a report's ID, revision, and exact payload before publication. Republishing
uses the same ID and content. The executor ignores lower/equal applied revisions
for state changes, while retaining evidence of conflicts. A conflicting payload
for an existing ID is a protocol error, not a duplicate to silently accept.
Validate cumulative snapshots against persisted evidence: a larger revision cannot
erase an execution, change immutable identity, or revert a known terminal physical
outcome. Conflicts pause affected work with an explicit error; monotonic revision
numbers alone are not sufficient evidence of a valid transition.

## Streams and consumers

Proposed streams: `LAB_COMMANDS` for `lab.commands.*` and `LAB_RESULTS` for
`lab.results.*`, with file-backed storage on the existing NATS volume.
Use one durable pull consumer filtered to each device's commands and one durable
executor consumer for results. Use explicit acknowledgements and queue-style
retention of unacknowledged messages; exact client configuration will be verified
against the pinned NATS version during implementation.

The executor provisions the result consumer and device-filtered command consumers
for the configured supported devices before authorizing work. Workflow reloads
must validate/provision any newly supported device before its first dispatch;
unknown device IDs fail validation rather than creating unconsumed commands.
A driver may start before provisioning and wait with backoff without accepting
physical work. Define one stream configuration shared by provisioning and tests.
Do not expire unprocessed commands
or results by age. Set explicit capacity limits and reject new publication when
full rather than evicting unprocessed work. Publisher failures remain durable
pending intent and apply backpressure. Initial deployment uses the single supplied
NATS server; replicated broker/high-availability guarantees are out of scope.

Proposed acknowledgement wait is 30 seconds with progress notifications every
10 seconds while a command is active. A missed notification may cause redelivery;
the job identity and driver state must still prevent re-execution. Keep transport
heartbeats/publication off the serial physical-execution wait so
an active job cannot prevent acknowledgement progress. Redelivered commands attach
to the existing job or replay its report; they do not enqueue another `Run` call.
Do not use a small delivery-count limit that abandons commands during a long
outage or hang.

| Boundary | Acknowledge only after |
| --- | --- |
| Outbox publication | Broker publication confirmation; then mark the outbox row published |
| Execute command | A terminal job report, including final data disposition, is durably accepted by JetStream |
| Driver report | Its evidence, deduplication marker, and resulting run/step changes commit together in Postgres |
| Malformed message | Persist a durable rejection record with diagnostic context before acknowledging it |

Nonterminal reports do not finish commands. Broker acceptance of a terminal report
allows the driver to acknowledge its command even if the executor is temporarily
down: the report remains queued for the durable result consumer. A failed command
acknowledgement is harmless; redelivery republishes the persisted report.

## Executor persistence and scheduling

Extend the schema without changing HTTP models or public Go signatures:

- Runs: cancellation intent and whether new scheduling is paused.
- Steps: current attempt/job, internal phase, and explicit unresolved/data errors.
- Attempts: immutable identity, ordinal, physical evidence, data state, applied
  report revision, retry due time, dispatch deadline, and nullable physical
  recovery deadline.
- Device reservations: at most one outstanding authorized job per device.
- Outbox: unique command ID, immutable payload, due time, publication state.
- Inbox/report records: unique report ID, payload identity, and processing outcome.

Persist execution evidence as a child collection keyed by execution ID; do not
store only one outcome or hash on an attempt. Each execution owns its measurement
state and object reference. The logical step explicitly selects the successful
execution used for completion. This supports part 6's history without discarding
unexpected duplicates.

Use uniqueness constraints for job ID, command ID, and `(run, step, attempt)`.
Apply transitions under transactions with a consistent lock order. One executor
still has concurrent HTTP handlers and background workers, so process count alone
does not make races safe.

When scheduling, atomically verify run intent, dependencies, retry eligibility,
and the device reservation; create the attempt, reservation, and outbox row in
the same transaction. The publisher retries committed outbox records until broker
confirmation. A crash between confirmation and marking published republishes the
same command, not a new attempt.

Keep one authorized job per device through final result/data handling for the
initial implementation. This intentionally blocks that device during upload or
unresolved recovery, simplifying ownership and bounding pending data. Other devices
and unrelated runs may progress. Choose eligible work deterministically, preferring
older ready runs; do not reserve a device while merely waiting for retry backoff.

On a retryable terminal report, record the outcome and release the reservation,
then persist retry eligibility/due time if budget and run intent allow. The next
attempt uses a new job ID. The scheduler rechecks cancellation at that point.

On startup, recover reservations, pending outbox records, retry deadlines, and
nonterminal attempts before scheduling new work. Do not infer failure from absence
of a result. Handle result identity/revision checks before updating any step.

Terminal reports release a reservation only when they match its job ID and carry
known terminal physical evidence plus final data disposition. Physical retries
are authorized only by the executor; the driver never allocates another job ID.
A terminal report can arrive before the driver's command acknowledgement: the
next command may queue, but the serial adapter cannot execute it concurrently.

## Driver execution and local spool

Give each driver its own persistent Compose volume mounted at `/spool`, separate
from the fixed instrument's ledger files. The adapter must refuse to accept work
if durable spool initialization fails. Do not silently fall back to temporary
storage. Acquire exclusive local ownership; running a second driver for the same
device is unsupported and must not be treated as a scaling mechanism.

For the initial implementation, use per-job records and separate measurement
files, written using temporary files, file sync, atomic rename, and parent-directory
sync. Persist references only after their target bytes are durable. Validate stored
hashes on recovery. Avoid independent unsynchronized writers to a job record.
An embedded database is an alternative if filesystem recovery becomes harder to
review; introducing one is not required by this design.

Processing sequence:

1. Validate command identity, then persist receipt and immutable job metadata.
2. Under serial device ownership, inspect spool and instrument status. If this job
   already executed, reconcile it. If evidence is intact, the job is absent, and
   the device is idle, it may start. Conflicting/corrupt evidence pauses the job.
3. Call `Run` only when no physical execution exists for the authorized attempt.
   An explicit `ErrBusy` or confirmed no-execution ledger-write error may be retried
   with the same job ID after reconciliation, without incrementing the attempt.
   Unknown return errors never justify repeating physical work. Observe `Status()`
   concurrently with the wait so a hang can
   settle even though `Run` never returns an outcome.
4. Prefer the returned outcome for capturing measurement bytes; ledger success
   alone must not race ahead and declare data loss while that outcome is still
   being delivered. Persist captured bytes immediately. Keep the outcome receiver
   alive through normal execution; a recovery timeout must not cancel the sole
   receiver and discard a later measurement. Stop a hung wait only after terminal
   hung evidence. Process shutdown retains the documented pre-spool data-loss risk.
   Fail closed on observed spool errors; never discard bytes to accept another job.
5. Persist and publish cumulative reports. If the device has physically completed
   a read, reconcile spool/object data before declaring the logical result final.
6. Keep the terminal report available for command redelivery. Do not call `Run`
   again for an already recorded job.

No automatic spool garbage collection is required initially. Retain job tombstones,
reports, and captured bytes so delayed redelivery and recovery remain safe. Monitor
capacity; on exhausted capacity stop accepting new jobs and expose the condition.
This is a bounded exercise deployment, not an indefinite retention solution.

## Object persistence

Use a stable key derived from validated generated IDs, for example
`runs/<run-id>/jobs/<job-id>/executions/<execution-id>/measurement.csv`.
Capture bytes and their hash first.
Retry upload with the same key and bytes. Verify downloaded bytes against the
ledger hash before publishing stored data and its reference. Never overwrite an
existing conflicting object silently; stop with a data-integrity error.

On restart, reconcile the spool and the expected object. An object matching the
ledger can recover a lost local spool reference. Missing bytes after physical
success cause explicit data loss only once local recovery is complete and object
storage can definitively establish absence. Storage unavailability leaves the job
running with a data-pending error; it does not authorize another plate read.

## Reconciliation, deadlines, and cancellation

Use part 1's capped exponential observation schedule and configurable recovery
deadlines. The executor persists an authorization-time dispatch deadline and
independently detects missing start evidence even if the driver cannot publish.
The driver derives the physical deadline from the ledger's physical start and the
configured timeout; persist it locally and include it in reports so the executor
can watch it too. Recover it from the same start timestamp after a crash, never
from reconnect time. A pre-start GraphQL physical deadline is null.

The executor watchdog must mark overdue work unresolved when no report arrives;
it cannot depend on receiving a driver's timeout event over a broken connection.
Clear only the error resolved by new evidence, and re-evaluate the other deadlines.
Late start evidence does not reset its physical start or authorize a replacement.
Persist deadlines; do not extend them on restart. After the deadline,
keep unresolved work running, pause new work for its run/device, and continue
bounded observation/publication attempts every 8 seconds while the service runs.
Clear the recovery error and resume policy evaluation only when evidence resolves
the uncertainty. A published old terminal report is still replayed and applied;
its lower revision cannot clear a distinct unresolved integrity conflict. Matching
terminal evidence can settle a timeout even if its observation timestamp predates
the executor's locally generated timeout. Local watchdog state is separate from
the driver's monotonically increasing report revision. Unrelated runs on other
devices can continue.

Already committed commands remain authorized after cancellation, even if their
publication was delayed. Publish and settle them normally. Do not delete outbox
rows or interpret cancellation as revoking a command that might already be in
flight. No additional cancellation message protocol is required initially.
Cancellation prevents new authorization and physical retries, while preserving
the outcomes of all previously authorized jobs.

There is no manual force-complete or force-retry operation in this scope. If data
or execution evidence remains unavailable, the run can remain unresolved. Operational
inspection uses explicit errors, logs, and the record; a future repair workflow
would require its own safety contract.

## Crash boundaries and expected recovery

| Failure window | Recovery |
| --- | --- |
| Before executor transaction commits | No authorized command exists |
| After commit, before publication | Replay outbox |
| After publication, before outbox update | Republish same command ID; driver deduplicates |
| After driver receipt, before physical start | Reconcile intact ledger under exclusive ownership; start only if no execution exists |
| After physical start, before adapter state update | Recover job from instrument ledger; never blindly call `Run` again |
| Driver killed while executing | Instrument restart records hung outcome; apply part 1 safety and retry limits |
| Read completed, before bytes persisted | Recover verified object if present; otherwise explicit data loss once absence is established |
| Bytes persisted, before/during upload | Replay spool upload without physical re-execution |
| Upload succeeded, acknowledgement lost | Read/verify stable object and publish the same data identity |
| Report published, command acknowledgement lost | Redelivery republishes terminal report without physical work |
| Executor result transaction committed, acknowledgement lost | Deduplicate report and acknowledge |
| Recovery timeout during partition | Keep running with error; reconcile late evidence |

Process restarts with intact volumes are covered. Lost/corrupt disk, deliberate
ledger modification, volume deletion, broker-volume loss, and multiple active
owners are not promised to recover automatically. Corruption should fail closed
where detected. The fixed instrument logs terminal-ledger write failures and may
retain newer evidence only in memory; after restart the ledger can therefore be
incomplete. Preserve any durably spooled outcome, flag contradictory evidence,
and do not claim disk-failure tolerance beyond the instrument interface.
The fixed instrument cannot atomically hand off measured bytes
to the spool, so that data-loss window remains an explicit limitation.

## Required verification and later specifications

Test every crash boundary above, duplicate commands/results, reordered reports,
capacity/backpressure, malformed messages, persistent retry budgets, cancellation
races, and concurrent runs. Validate the spool's interrupted-write recovery and
hash checks separately from process-level chaos tests. Assert that missing reports
or unavailable storage never produce false completion or duplicate physical work.

Part 3 will specify automated test orchestration, fault injection, isolation, and
artifact collection. Stream API details and numeric capacity defaults must be
checked during implementation; no test result is claimed by this document.
