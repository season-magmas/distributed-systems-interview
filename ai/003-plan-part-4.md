# Part 4: first complete execution path

Status: detailed implementation specification, 2026-10-24. Planning only; no
application changes, database resets, or runtime tests have been performed here.

## Agreed choices

- Recreate disposable databases when the schema changes. Versioned upgrade
  migrations and preservation of old development records are outside this scope.
- Implement in small tested commits: persistence and messaging, driver execution
  and data handling, then executor scheduling, with acceptance at the end.
- Preserve the contracts and safety rules agreed in parts 1–3. A first working
  path must use the intended durable architecture; it must not depend on replacing
  a temporary in-memory protocol later.

The milestones below divide those groups into reviewable commits. Combine or
split a milestone only when doing so keeps builds working and changes coherent.
Planning remains separate from implementation until the remaining parts are reviewed.

## Scope and completion gate

Deliver one complete path from the existing create/start HTTP API through durable
dispatch, physical execution, verified data storage, and truthful run completion.
Support concurrent workflow runs with exclusive ownership of each device and
parallel progress on different devices.

Implement required identity, persistence, deduplication, bounded retry state,
cancellation intent, and result application from the start. Part 5 exercises and
hardens fault recovery; it does not excuse unsafe behavior in this slice. GraphQL
is specified and added in part 6 using the same application operations.

The part 4 gate is all seven checks in the unchanged acceptance script, plus
focused tests of persistence, serialization, cancellation, and data integrity.
Passing this gate alone is not evidence of chaos resilience.

## Code organization and compatibility

- Keep the executor and driver entry points and public signatures unchanged.
- Put shared wire types, identity validation, and protocol constants in a small
  package under `services/common/`; avoid importing either executable package.
- Keep executor SQL, scheduling decisions, outbox publication, and result handling
  in separate focused files within `services/executor/`.
- Keep driver spool operations, job coordination, data upload, and report
  publication separate within `services/driver/` or a narrowly scoped subpackage.
- Retain the existing storage wrapper, adding only behavior actually needed for
  verification. Keep the fixed instrument implementation untouched.
- Add narrow interfaces where tests need to control transport, time, or storage
  failures. Avoid a generic service framework or mock interfaces for every type.
- Use named durations and limits, explicit errors, small functions, and meaningful
  behavior tests, following `AGENTS.md`.

## Commit 1 — Protocol and pure transition rules

Define versioned command/report envelopes from part 2, stable identities, required
fields, and validation rules. Model attempt state separately from logical step
state. Centralize physical retry eligibility, attempt limits, backoff calculation,
dependency readiness, and public-state projection where they can remain pure.

Tests cover invalid messages, identity mismatch, duplicate/reordered reports,
retryable versus unsafe outcomes, three-attempt exhaustion, and non-regression of
known physical facts. Do not test only JSON round trips or mirror each line of
implementation. Establish deterministic time inputs for retry-policy tests.

Gate: focused unit tests pass; all existing packages still compile.

## Commit 2 — Schema and transactional persistence

Extend `db/init.sql` with attempts, reservations, outbox, report deduplication,
and run cancellation/pause state. Add uniqueness and foreign-key constraints for
the identities defined in part 2. Preserve fields required by existing HTTP views.

Implement transactions that authorize a job together with its reservation and
outbox intent, and apply a driver report together with its deduplication marker
and state transitions. Include persisted retry due times and recovery deadlines.
Persist blocked-work reasons without inventing physical timestamps.

Use real disposable Postgres integration tests for rollback, uniqueness, concurrent
reservation attempts, duplicate reports, and cancellation versus authorization.
SQL-string mocks alone cannot establish these properties.

Schema resets recreate only explicitly disposable resources. The harness owns its
test volumes and may reset them automatically. A normal application startup must
never drop a database. If an old incompatible schema is detected, fail with a
clear setup instruction rather than partially operating against it. Document the
test-stack reset process; do not delete unrelated volumes to apply `init.sql`.

Gate: clean initialization and transaction integration tests pass.

## Commit 3 — JetStream provisioning and delivery

Implement idempotent stream/consumer setup, explicit acknowledgements, publication
confirmation, and the outbox publisher. Verify existing broker configuration is
compatible; fail clearly rather than silently changing retention destructively.
Add result consumption with transaction-before-ack behavior and durable handling
of invalid/conflicting messages as specified in part 2.

Test against real JetStream: duplicate publication, acknowledgement loss, retained
pending messages, and durable consumer reconnect. Test a failed publish leaving
outbox intent pending and a post-publication local failure reusing the command ID.
Do not rely on the broker deduplication window for physical execution safety.

Gate: transport integration tests pass; no application command can be acknowledged
before its required durable boundary.

## Commit 4 — Driver spool and physical execution

Add per-driver durable spool volumes and validated configuration. Implement atomic
job/byte/report persistence, exclusive local ownership, and recovery inspection.
Serialize device execution across commands and reconcile each job against the
instrument ledger before any call to `Run`.

Support concurrent status observation and outcome waiting without racing a completed
plate-read outcome into a premature data-loss report. Capture bytes immediately,
persist report revisions before publishing, and retain terminal records for replay.
Handle busy devices without counting an extra physical attempt.

Use the real instrument with temporary ledger directories for local execution
tests. Prove duplicate commands do not repeat physical work, busy handling preserves
identity, persisted spool records reload, and an interrupted spool write cannot
be mistaken for a complete measurement. Keep timing-dependent tests bounded.

Gate: driver tests pass; successful physical execution has a durable replayable
record before the command is eligible for acknowledgement.

## Commit 5 — Verified measurement storage

Store spooled bytes under the stable job-specific key. Verify the stored bytes
against the ledger hash before publishing a stored-data report. Retry the same
bytes after transient upload failure. Keep the step running while data is pending;
report irrecoverable byte loss separately from physical success.

Test real object-store upload/read/hash verification and stable-key replay.
Use controlled storage failures for retry behavior, hash mismatch, and unavailable
versus definitively absent objects. Verify none of those paths starts another read.

Gate: a real plate-reader result can produce a valid `s3://bucket/key` whose
downloaded content matches its independent ledger hash.

## Commit 6 — Executor scheduling and API integration

Connect `StartRun` to durable scheduling, keeping repeated starts idempotent and
terminal runs terminal. Reuse `NextSteps` for within-run eligibility and enforce
cross-run device reservations in Postgres. Wake scheduling after relevant state
changes, with a bounded periodic reconciliation pass so missed wakeups cannot
stall a run forever.

Implement `CancelRun` as persisted intent: prevent new authorization, suppress
future retries, and settle already committed commands with their actual results.
Apply run aggregation and step projection from part 1. Preserve the HTTP route,
JSON, timestamp, and error contracts used by the checks.

Start background loops under an owned context; `Close` cancels and joins them
before dependencies close. Physical work surviving a cancelled wait is reconciled,
not reported as stopped. Startup must recover durable intent before new scheduling.
Avoid requiring a temporarily unavailable object store to erase or fabricate job
state; startup dependency handling is exercised further in part 5.

Tests cover simultaneous starts, shared-device contention, separate-device overlap,
run/step agreement, cancellation races, and repeated lifecycle operations. Test
that all physical executions for a run remain accounted for through HTTP views
and retained attempt evidence.

Gate: full Go tests and applicable race checks pass; multiple workflows progress
without overlapping physical work on one device.

## Commit 7 — End-to-end milestone and evidence

Use the isolated harness from part 3 to start a fresh fault-free stack and run
`scripts/acceptance.sh` unchanged. Verify all seven checks, especially physical
parallelism and stored measurement hashes. Also run a two-run contention case and
basic cancellation cases; retain artifacts for failures.

Run coverage inspection and the applicable unit/integration suite. Close meaningful
coverage gaps around authorization, result application, and data handling rather
than adding tests solely to raise a percentage. Record the exact commands, outcomes,
runtime, and remaining fault coverage in `NOTES.md`.

Gate: acceptance and focused scenarios pass, documentation matches implemented
behavior, and no known safety regression is deferred under a happy-path label.

## Commit discipline and handoff

Each commit includes the tests for its behavior and passes formatting and whitespace
checks. Stage only intended changes. Keep failures reproducible and fix them before
claiming a milestone complete. Do not claim future suite coverage as existing
validation, and do not amend away useful failure evidence from the notes.

Part 5 then works through deterministic recovery scenarios and seeded chaos. Its
bugs should produce focused regression tests and small fixes following the same
commit discipline. Implementation time is recorded as actual time spent, separate
from planning and testing time.
