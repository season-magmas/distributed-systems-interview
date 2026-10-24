# Part 5: recovery hardening and fault validation

Status: detailed implementation specification, 2026-10-24. Planning only; fault
experiments and recovery validation have not been run.

## Agreed decisions

- Include NATS and Postgres process restarts with intact volumes in the extended
  suite, alongside the previously specified application, network, and storage faults.
- Stop a suite after its first failed scenario. Always collect bounded diagnostics,
  stop fault subprocesses, and clean test-owned resources before exiting nonzero.
  Mark remaining scenarios not run; do not report them as passed or as covered.
- Keep small tested commits and the execution/recovery rules from parts 1–4.

An expected instrument failure is not a failed test. Fail-fast applies when an
assertion, setup operation, injection precondition, or cleanup step fails. A run
that truthfully fails after an unsafe instrument outcome can pass its scenario.

## Recovery workflow

Start from part 4's tested execution path. Work through targeted scenarios in the
order below before seeded random chaos. For each failure:

1. Preserve exact configuration, seed, run/job IDs, observations, and logs before
   cleanup. Identify the first violated invariant, not just the final timeout.
2. Reproduce using the named scenario or a smaller controllable component test.
   A missed injection is a harness problem to fix, not evidence of recovery.
3. Add a focused regression test, then fix the underlying boundary or transition.
4. Run that regression and the affected integration/scenario checks. Commit a
   coherent tested fix and update `NOTES.md` with evidence and remaining limits.
5. Resume validation explicitly. Final completion requires a complete passing
   routine suite and extended suite for the final implementation revision, with
   GraphQL checks added after part 6. Part 5's own gate is the explicitly selected
   HTTP/ledger stage; it does not claim GraphQL coverage before that feature exists.

Do not automatically rerun failed scenarios until they happen to pass. Retain
failed attempts and distinguish nondeterministic faults from application bugs.
Independent successful checks need not be repeatedly run after unrelated changes,
but any change invalidating a prior result requires the affected check again.

## Phase A — Delivery, restart, and ordering

Exercise outbox publication before/after broker confirmation, command redelivery,
driver receipt before physical start, result publication before command ack, and
executor commit before result ack. Use controlled component boundaries for precise
windows and process kills for broader end-to-end verification.

Verify that:

- Republished commands retain job identity and never cause duplicate execution.
- Repeated results retain their identity and are idempotent after database commit.
- Out-of-order reports cannot regress physical facts or complete the wrong attempt.
- Invalid/conflicting messages are recorded durably without silently losing a
  legitimate outstanding command or falsely completing its step.
- Recovered reservations and retry due times survive restarts; attempt limits
  cannot reset and an old attempt cannot release a newer attempt's reservation.
- Publication and result-processing failures leave recoverable durable intent.

## Phase B — Partitions and unavailable dependencies

Disconnect the executor and each driver while a job is active. Observe that
isolated instruments continue working, reconnect, and reconcile ledger evidence.
Extend a partition beyond the configured recovery deadline: the affected work
must remain running with an explicit error and no speculative new authorization.
After reconnection, late evidence must resolve that uncertainty.

Exercise storage outages during measurement capture and upload. Captured bytes
must survive locally, upload retries must use the same bytes and key, and completion
must wait for verified storage. Test storage remaining down across executor or
driver restart; application readiness/retry behavior must not discard work or
mistake unavailable data for lost data. The current executor startup calls
`EnsureBucket`; review that dependency so transient storage failure does not make
recovery impossible. All retry loops must have bounded individual calls and
cancellable waits, with backoff rather than busy looping.

## Phase C — Physical failure, hang, and death

Use failure fixtures and persistent faults to verify safe-device retries and the
three-attempt limit. Liquid-handler failure/hang never causes automatic repeat
dispensing. Observe terminal ledger evidence and device idleness before retrying
a confirmed hang on an incubator or plate reader.

Cover real 30-second hangs and longer configured hangs with suitable recovery
deadlines. Status polling must recover even when `Run` never sends an outcome.
Status checks and busy responses consume no physical attempts. Verify backoff and
attempt count remain intact across restart during a retry wait. Also delay
initial dispatch beyond 45 seconds, then execute a real 30-second hang: its physical
recovery window starts at physical execution, while the earlier dispatch timeout
remains an uncertainty alert until evidence arrives.

Kill drivers during execution and after capture. The instrument and adapter share
a process, so startup ledger reconciliation reports interrupted execution as hung.
After durable capture, recover the original bytes from the spool. Before capture,
successful physical execution with no recoverable bytes must become explicit data
loss, with no automatic re-read. Use a controlled adapter boundary for this narrow
window; never modify the fixed instrument or fabricate measurement bytes.

## Phase D — Cancellation, contention, and integrity

Race cancellation with authorization, outbox publication, execution, retry due
time, and upload. Commands committed before cancellation may still execute;
settle their actual outcomes. No additional jobs or retries may be authorized
after cancellation intent. Keep cancellation nonterminal while authorized work
is unresolved. Verify repeated requests are idempotent.

Run multiple workflows sharing devices. Check physical intervals for overlap on
the same device and ensure independent devices can make progress. Exercise late
events from older attempts and a run failure while another step is still active.

Test spool corruption and capacity exhaustion, mismatched object hashes, and
broker capacity backpressure using controlled test-owned resources. Expected
behavior is an explicit error and safe pause, not new execution or false success.
Recovery from destroyed persistent evidence is outside the automatic guarantees.

## Phase E — NATS and Postgres restarts (extended suite)

Add harness-owned restart helpers for these infrastructure services. Keep
`chaos.sh kill` scoped to executor/drivers as specified in part 3; the new helpers
must use the same isolated Compose project and ownership checks.

Kill and restart the existing service container with its volume intact. Do not
recreate schema, clear broker streams, or reset consumers during a restart test.
Prove the service went down and returned to readiness; retain restart timestamps.

| Scenario | Injection point | Required evidence after recovery |
| --- | --- | --- |
| NATS restart with pending dispatch | A committed outbox command or unacknowledged delivery exists | Outbox resumes publication; durable consumer recovers; one physical execution per job |
| NATS restart with pending report | Driver has captured a terminal report | Spool republishes if needed; executor records the outcome once |
| Postgres restart during authorization | Transaction boundary around attempt/reservation/outbox creation | All three commit together or none do; uncertain commit is reconciled before new authorization |
| Postgres restart during result application | Report received but transaction outcome uncertain | Redelivery resolves commit ambiguity; no duplicate transition or lost result |
| Infrastructure restart with active work | Instrument already executing | Physical work remains accounted for and the final record agrees with the ledger |

Test client reconnects and database-pool recovery, including interrupted calls.
Treat a connection failure during commit as an unknown commit outcome until durable
state is inspected. Never assume rollback merely because the client saw an error.
Restore application processes if the design requires restart on dependency loss,
but make that requirement explicit in the scenario and notes.

These tests cover process restarts with intact storage. They do not establish
survival of broker/database volume loss, host power loss, replicated failover,
or multiple active executor/driver owners.

## Phase F — Seeded and combined chaos

Run the routine fixed-seed case after deterministic cases pass. Extended testing
adds the seeds and combinations from part 3, including captured data plus storage
outage plus driver restart. Keep work active across the injection window and allow
a bounded recovery period after faults stop before final assertions.

Record actual fault timing and observed ledger outcomes. A seed identifies a
fault schedule, not a guarantee that a particular crash boundary was exercised.
Use component regressions to cover any boundary the random runs did not reach.

## Fail-fast reporting and completion gate

On the first failure, stop scheduling scenarios immediately. The current failure
still gets diagnostics and cleanup, each with its own timeout. Preserve the original
failure as the primary cause and append diagnostic/cleanup failures separately.
Return nonzero even if cleanup succeeds. Do not start a final acceptance case
after an earlier suite failure; it runs only on a subsequent successful suite.

Report completed scenarios, the first failure, and remaining scenarios as not run.
If the process is interrupted, record interruption and cleanup status rather than
inventing an assertion failure or successful completion.

Part 5 is complete when all required component regressions, routine scenarios,
extended HTTP/ledger scenarios (including both infrastructure restarts), relevant
race checks, and final fault-free acceptance pass. Record precise evidence and actual runtime
in `NOTES.md`. Any missing prerequisite, unexecuted scenario, or known violation
must remain explicitly outstanding; it cannot be hidden behind a passing subset.
GraphQL validation follows in part 6; part 7 gates final submission on the complete
suite with GraphQL enabled.
