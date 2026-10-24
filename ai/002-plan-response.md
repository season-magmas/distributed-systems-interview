# Proposed implementation plan

Status: proposed for discussion, 2026-10-24. Implementation has not started.
D1–D4 are agreed as detailed in `003-plan-part-1.md`; uncertainty presentation
(D7) remains proposed. Other recommendations remain proposals. We will review and refine the steps
in order before proceeding to implementation.

## Objective and constraints

Make the scientific record agree with physical execution despite network loss,
process death, instrument failures and hangs, and object-storage outages. Add
GraphQL and automated system tests, while preserving the existing HTTP API,
public Go signatures, model shapes, and status values.

The fixed instrument implementation remains unchanged. Only the executor uses
Postgres. Independent devices should run concurrently; each device must remain
exclusive across all runs, not just within one workflow.

## Findings that shape the design

- The current executor and adapter are unfinished. Acceptance is a happy-path
  floor, not a recovery test suite.
- `NextSteps` considers device occupancy within one run. Cross-run arbitration
  needs an additional design.
- The instrument records a supplied job ID, but does not deduplicate calls to
  `Run`. Reusing an ID alone does not prevent repeat physical execution.
- Cancelling a context stops waiting, not physical work. Network isolation also
  leaves the instrument running.
- Driver and simulated instrument share a process. Killing a driver simulates
  instrument death; reloading its persistent ledger marks interrupted jobs hung.
- A hung job eventually frees the device and updates the ledger, but never
  returns an outcome to its waiting caller. Recovery must inspect status.
- The ledger preserves measurement hashes, not measurement bytes. A crash after
  measurement but before the adapter persists the bytes can lose data permanently.
  We cannot promise lossless recovery for this window with the fixed interface.
- `chaos.sh` supports executor kill/restart, network disconnects, storage outages,
  and seeded random combinations. Its kill command currently rejects drivers.
  Instrument failure/hang percentages are configured separately through Compose.

## Decisions to discuss

| ID | Question | Recommendation | Alternative and tradeoff | Status |
| --- | --- | --- | --- | --- |
| D1 | How should commands and results travel? | Durable JetStream delivery, plus a Postgres transactional outbox | Core NATS with persisted reconciliation reduces broker setup but needs explicit recovery for every lost request/reply | Agreed: JetStream and Postgres outbox |
| D2 | When may physical work repeat? | Bounded retries with exponential delays, starting at 0.5 seconds and doubling to an 8-second cap, with a recovery window exceeding the configured hang duration; proposed eligibility is confirmed retryable failures | Stop after every failure for simplicity, or explicitly permit safe-device re-execution with a new attempt and full history | Agreed: three physical attempts; retry confirmed hangs on safe devices; see part 1 |
| D3 | How should clients follow runs? | GraphQL queries and mutations with polling initially | Subscriptions improve responsiveness but add connection and replay semantics | Agreed: polling for simplicity |
| D4 | What should cancellation mean? | Stop authorizing new work, reconcile already dispatched work, preserve its actual outcomes, and settle cancellation after active work is accounted for | Immediate terminal cancellation is faster to report but can hide continuing physical work | Agreed; detailed in part 1 |
| D5 | What deployment scope do we support? | One executor and one adapter per device initially, with concurrent runs and durable recovery | Multiple active instances require ownership/fencing design, especially around physical execution | Review in step 2 |
| D6 | How should driver death be injected? | Extend `chaos.sh kill` to the existing driver allowlist, preserving executor behavior | A companion helper avoids editing the supplied script but splits fault injection across tools | Review in step 3 |
| D7 | How should uncertainty appear in the fixed HTTP model? | Explicit error reasons and conservative status mapping; expose execution and data state separately in GraphQL | Leaving unresolved work running avoids overstating failure but needs a clear operational resolution path | Review in step 1 |

Record each answer here with its rationale. Carry agreed decisions and eventual
evidence into `NOTES.md`; keep proposals distinguishable from implemented behavior.

## Step 1 — Define the record and recovery contract

Agree on a small state-transition table before selecting implementation details:

- Separate a logical step, an execution attempt, and a delivered message.
- Define job/attempt identities: redelivery retains the same attempt; an approved
  physical retry creates a new attempt.
- Define evidence required for completion, failure, cancellation, and retry.
  A timeout alone never authorizes a new physical execution.
- Define how dispatch, physical execution, data persistence, and uncertainty map
  to the five fixed HTTP statuses. Do not fabricate physical timestamps.
- Define cancellation races for queued commands and already running instruments.
  Account for dispatches already committed when cancellation arrives.
- Separate recovery/status checks from new physical attempts. Waiting for a busy
  or hung instrument must not consume the physical-attempt budget or call `Run`
  again. Poll its status and reconcile the existing job's ledger entry.
- Use bounded exponential recovery delays of 0.5, 1, 2, 4, 8, 8, 8, 8 seconds.
  These allow eight checks over 39.5 seconds, covering the default 30-second hang
  with margin.
- Make the recovery deadline configurable and require it to exceed the configured
  `HANG_DURATION` with margin (proposed default: 45 seconds for a 30-second hang).
  For longer configured hangs, extend the capped-delay schedule and deadline.
  Bound status-call durations within that deadline as well.
- Retry physical execution only after status confirms the instrument is idle,
  the previous attempt has a known terminal outcome, and the agreed safety policy
  permits another attempt. An idle device alone does not prove retry safety.
  Hangs on a liquid handler must not trigger an automatic repeat dispense.
- Allow at most three physical attempts; confirmed hangs on incubators and plate
  readers are retryable after the device becomes idle. On recovery
  deadline exhaustion, retain the unresolved evidence and stop for review rather
  than infer that physical work stopped.
- Define plate-reader completion: the selected execution completed and its actual
  bytes are durably stored and match its ledger hash.
- Explicitly document the unrecoverable measurement window. A replacement read
  cannot recover the original measurement or be presented as that measurement.

Deliverable for review: transition table, invariants, and answers to D2, D4, D7.

## Step 2 — Agree on topology, persistence, and recovery boundaries

Proposed topology using the agreed JetStream and Postgres outbox approach:

```text
HTTP / GraphQL -> executor -> Postgres run/step/attempt state + command outbox
                                  |
                             outbox publisher
                                  |
                       JetStream device commands
                                  |
                     one serial adapter per device
                        |                      |
                  fixed instrument       durable result/data spool
                                               |
                                  S3 bytes + JetStream results
                                               |
                               executor transaction -> result ACK
```

- Tentative subjects: `lab.commands.<device-id>` and `lab.results.<device-id>`.
  Refine stream retention, durable consumers, filters, and acknowledgement rules.
- Commit state changes and outbound intent together in Postgres. Retry publishing
  the same intent after crashes; tolerate duplicates beyond broker dedup windows.
- Drivers serialize command handling and consult their durable state plus the
  instrument ledger before calling `Run`. Cover every crash boundary between
  command receipt, physical start, result capture, and publication.
- Persist adapter results and received measurement bytes on a durable volume.
  Retry uploads using stable object keys and verify hashes before exposing data.
- Proposed command acknowledgement boundary: a recoverable result has been
  durably published. Manage long-running delivery deadlines without starting
  duplicate work. Refine how data-upload delays affect device availability.
- Apply results idempotently in a Postgres transaction, then acknowledge them.
  Reject stale attempts and prevent late events from regressing recorded facts.
- Reconcile outstanding work on startup and reconnect; distinguish missing
  messages from incomplete physical execution and missing data.
- Define schema additions for attempts, outbox, result deduplication, and any
  cancellation intent. Define scheduling across simultaneous runs.

Deliverable for review: message contracts, state ownership, acknowledgement table,
crash-recovery table, and answers to D1 and D5. Do not claim that durable messaging
alone gives exactly-once physical execution.

## Step 3 — Design the automated system-test harness

Build the harness early so it can expose failures before recovery is complete.
Suggested entry point: `./scripts/system-test.sh`, orchestrating Compose and a Go
test runner for API, ledger, and object assertions. Exact organization is open.

The harness should:

1. Use a dedicated test stack and owned volumes. Account for the Compose file's
   fixed ports and subnet; a project-name override alone does not isolate those.
2. Wait for readiness, configure fault settings explicitly, create identifiable
   runs, and wait for observable execution milestones before targeted faults.
3. Invoke the provided chaos script for supported faults. Extend its driver kill
   support if D6 is accepted. Apply failure/hang settings before starting runs.
4. Bound every wait, tolerate expected temporary unavailability, and fail with
   useful diagnostics if the system does not settle within the scenario deadline.
5. Compare HTTP and GraphQL records with the independent instrument status ledger
   and downloaded object bytes. Do not trust executor status as the test oracle.
6. Save run IDs, seeds, fault timing, configuration, API snapshots, ledger entries,
   object hashes, and service logs. Report failures with a nonzero exit status.
7. Restore networks, services, and fault settings on exit. Clean up only test-owned
   resources; preserve failure artifacts and never reset an unrelated stack.

### Required scenario matrix

| Scenario | Injection | Main assertions |
| --- | --- | --- |
| Happy path | Faults disabled; provided acceptance script | All seven checks pass, including overlap and measurement hash |
| Network interruption | `chaos.sh disconnect` for executor and each driver | Reconcile after reconnect; no duplicate execution from lost replies |
| Executor death | `chaos.sh kill executor` at dispatch and result milestones | Recover durable intent and results without false completion |
| Instrument/driver death | Proposed `chaos.sh kill <driver>` during execution and data handling | Ledger restart outcome is reflected; unsafe work is not repeated |
| Storage outage | `chaos.sh storage-down` during plate read/upload | Retain captured bytes; recover upload; never expose missing data as complete |
| Instrument failure | Each device's `*_FAIL_PCT`, including deterministic 100% cases | Respect retryability and retry bounds; retain attempt history |
| Instrument hang | Each device's `*_HANG_PCT` and `HANG_DURATION` | Cover the default 30-second hang and a longer configured hang; recover after idle/terminal ledger evidence, never retry while busy, and apply the agreed safety policy |
| Seeded chaos | `chaos.sh random --seed 7 --for 60`, then additional fixed seeds | Maintain record invariants; settle after faults stop |
| Concurrent runs | Multiple active workflows sharing devices | No device overlap; dependency order holds across retries |
| Cancellation | Before dispatch, while queued, and while executing | No hidden work; preserve actual outcomes and agreed cancellation semantics |

Use both `CHAOS_SEED` for instrument behavior and the chaos script's `--seed` for
fault scheduling. Save observed timing: identical seeds do not make concurrent
process scheduling identical. Keep targeted regressions alongside random tests.

Fault scenarios need their own oracle: a truthful failed run may be correct.
The happy-path acceptance script requires completion and no repeated steps, so it
cannot directly judge scenarios where an approved retry legitimately executes.
Verify both directions of truth: every reported completion has physical evidence,
and every physical execution is accounted for, including failed and extra attempts.

Process-level chaos cannot reliably hit every instruction-sized crash window.
Use focused component tests with controllable adapter/storage boundaries for
those cases, without modifying the fixed instrument.

Deliverable for review: harness lifecycle, scenario expectations, artifact format,
and answer to D6. Then implement failing baseline scenarios alongside each feature.

## Step 4 — Implement the smallest complete execution path

After the preceding design review, implement persistence and message contracts,
executor scheduling/outbox, serial driver handling, result application, and data
upload. Include meaningful unit and integration tests with each component.

First milestone: the provided acceptance script passes with faults disabled,
physical timestamps are reflected accurately, and separate devices overlap.

## Step 5 — Implement and prove recovery

Work through deterministic fault scenarios individually: duplicate delivery,
executor restart, disconnects, storage outage, driver death, failure, hang, and
cancellation. Add regression tests for each discovered bug. Exercise late and
reordered results, cross-run contention, and crash boundaries around persistence
and acknowledgements. Finish with seeded random chaos and a clean acceptance run.

Milestone: each scenario meets its agreed invariant and recovery deadline, or its
specific remaining limitation is documented with evidence.

## Step 6 — Add the GraphQL interface

Agree on schema and library before coding. Suggested operations are workflows,
run lookup, paginated run listing, and create/start/cancel mutations. Expose steps,
dependencies, attempts, physical outcomes, timestamps, errors, and data references
so clients can distinguish execution success from data availability.

Use the same application operations as HTTP. Decide nullability, error handling,
pagination, and polling behavior (D3); subscriptions remain a separate option.
Test schema behavior, mutation races, and consistency with the HTTP record.

Milestone: scientists can inspect the record and tools can start and follow runs
through `/graphql` on the existing executor port.

## Step 7 — Finish evidence and submission notes

Run the appropriate Go suite, coverage checks, concurrency race checks, acceptance,
and automated fault scenarios. Record actual commands and outcomes in `NOTES.md`,
including failures and untested boundaries. Include the topology, alternatives,
GraphQL rationale, actual time spent, exercise feedback, and AI conversations.

Review the final record against the instrument ledger and stored bytes. State
limitations explicitly, especially lost measurement data and unsupported deployment
or storage-loss scenarios. Do not infer successful testing from the plan itself.

## Review sequence and current decision log

Confirmed from the request: plan first, discuss options, refine each step before
implementation, include automated chaos testing, and commit this proposed plan.

User decisions recorded on 2026-10-24:

- D1: use JetStream for durable messaging and a Postgres outbox for outbound intent.
- D2: use bounded instrument retries with exponential delay. The user clarified
  that recovery must outlast the default 30-second hang. Extend the proposed
  recovery checks to 0.5, 1, 2, 4, 8, 8, 8, 8 seconds (39.5 seconds total), with
  a configurable deadline proposed at 45 seconds and adjusted for longer hangs.
  Recovery checks do not count as physical attempts. Part 1 confirms three total
  physical attempts and retries for confirmed hangs on safe devices. Lost
  measurement bytes cause explicit step failure without automatic re-reading.
- D3: use GraphQL polling to keep the interface as simple as possible.

D4 is agreed: cancellation stops new work and settles outstanding work while
preserving actual outcomes. Part 1 is specified in `003-plan-part-1.md`; its D7
status mapping is proposed for review. D5 and D6 remain queued for parts 2 and 3. These decisions authorize
plan refinement; implementation still follows the requested step-by-step review.

Validation for this document: repository/source review and whitespace checks only.
No implementation, acceptance tests, or chaos experiments have been run for this plan.
