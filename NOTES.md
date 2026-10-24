# Implementation notes

This implements the lab executor/driver exercise using JetStream, a Postgres
outbox, persistent driver spools, and GraphQL queries/mutations with polling.
All 33 recorded system scenarios passed across the retained/resumed campaign
segments described below. Known limits remain explicit.
The detailed design and interview decisions are in `ai/003-plan-part-1.md` through
`ai/003-plan-part-7.md`. Reproduction and failure evidence are in
[`docs/validation.md`](docs/validation.md).

## Plan
* Use Astra to help create a high level plan. Asking me question on how we should proceed. These are captured in ai/002-plan-response.md.
* Each part was then planned in detailed using a simple Q/A process. These are captured in ai/003-plan-part-X.md.
  * Rejected suggested use of graphql mutations as we already have existing HTTP methods and want to keep things as simple as possible.
    * This was then reverted on plan review

## Implementation
* Use Sol to implement the plan as above.
* Taking a break while Sol gets busy.

## Testing
* Manual testing using provided acceptance.sh test and chaos.sh with multiple concurrent workflows in progress

## Extension
* Added simple UI to aid testing and verify sane outputs

## 1. Topology and state

```text
HTTP / GraphQL -> executor -> Postgres run record, attempts, reservations, outbox
                                |                          ^
                         outbox publisher                  |
                                v                          |
                   LAB_COMMANDS / lab.commands.<device>    |
                                |                          |
                      serial instrument adapter            |
                        |                 |                 |
                  fixed instrument    durable spool        |
                                          |                |
                                    verified S3 bytes      |
                                          |                |
                   LAB_RESULTS / lab.results.<device> ------+
```

- Both streams use file storage, work-queue retention, no age expiry, a 64 MiB
  capacity and rejection of new publications when full. Capacity errors retain
  pending intent rather than evicting unprocessed messages. NATS itself is the
  supplied single server with a persistent volume; this is not replicated HA.
- Command consumers are durable and filtered to one device; the executor has a
  durable result consumer. Acknowledgements are explicit, with a 30-second wait
  and command progress notifications every 10 seconds. Physical safety does not
  depend on the broker's finite deduplication window.
- Creating an attempt, reserving its device and writing its outbound command are
  one Postgres transaction. Outbox publication waits for broker confirmation;
  publication ambiguity reuses the same command/job identity.
- The driver serializes device work and checks its spool plus instrument ledger
  before execution. A command is acknowledged only after its terminal report and
  final data disposition are durably accepted by JetStream. A result is acknowledged
  only after evidence/deduplication/state changes commit in Postgres.
- Each named driver has its own persistent `/spool` volume. A per-job atomic JSON
  record contains immutable command identity, received measurement bytes, and the
  latest replayable report. Writes use file sync, rename and directory sync; an
  exclusive lock rejects a second local owner. Corruption/capacity errors stop work.
- Postgres stores each public run and scheduling projection in one JSON aggregate,
  plus separate attempts, reservations, outbox, inbox and rejection tables. This
  simplifies consistent HTTP snapshots. GraphQL batches attempts in a repeatable-read
  snapshot. A transaction advisory lock serializes state transitions for the
  agreed single-executor deployment. This is an intentional simplification of the
  plan's initially separate run/step tables, not a distributed ownership mechanism.
- The instrument ledger remains the physical evidence. Job IDs identify authorized
  attempts, execution IDs identify actual physical work, and report IDs/revisions
  identify transport updates. Redelivery never creates another physical attempt.

## 2. Decisions and alternatives

JetStream makes offline commands/results durable. The Postgres outbox closes the
state-change/publication gap; Core NATS alone would need custom replay for every
lost request and reply. Neither durable messaging nor reusing a job ID alone stops
repeat physical execution: the fixed instrument records IDs but does not deduplicate.

There is one executor and one driver per device, with concurrent runs and durable
device reservations. Reserving through final data handling is conservative: an
upload outage blocks that device, while unrelated devices/runs may progress. It
keeps pending measurements bounded without introducing a separate upload scheduler.

A step gets three total physical attempts. Only confirmed retryable failures/hangs
on incubators/readers permit another attempt; liquid handling never repeats after
failure/hang. Physical retry delays are 0.5 and 1 second for the two possible retries.
Recovery observations separately back off 0.5, 1, 2, 4, 8 seconds, capped at 8 seconds;
normal outcomes wake the adapter immediately. Checks/busy responses consume no
physical-attempt budget. Retry due times survive executor restart.

A dispatch deadline alerts on missing start evidence without expiring an authorized
command. Physical recovery derives from the ledger start with at least
`max(STEP_DURATION, HANG_DURATION) + 15s` (45 seconds with default settings).
After expiry, unresolved work stays running with an explicit error, no speculative
retry, and affected dispatch paused. The executor has its own watchdog so a broken
connection cannot prevent the unresolved error from appearing. Late evidence resolves it.

Cancellation prevents further authorization/retries and settles committed commands
with their actual outcomes. It does not stop physical work. Cancelling during retry
backoff preserves the last failed physical outcome and settles without waiting for
work that was never authorized. Repeated start/cancel operations are idempotent.

Measurement keys include run, job and execution IDs. The driver persists actual
bytes, verifies downloaded SHA-256, and then exposes `s3://bucket/key`. Upload replay
uses the original measurement. An existing verified object can resolve a lost upload
response without another PUT. Confirmed missing bytes after physical success fail
the logical step with `measurement_data_lost`; a replacement read is not the original
measurement and is not silently substituted.

The database is disposable for this exercise: schema changes require recreating
explicitly disposable resources, without versioned upgrades. Application startup
never drops a database. Implementation followed small compiling test commits and
implementation commits, with regressions added for observed failures.

## 3. GraphQL interface

The schema is defined in [`services/executor/graphql.go`](services/executor/graphql.go)
and served at `/graphql` on the executor's existing server. It uses graphql-go,
with introspection, JSON POST, variables, and operation names. Queries include
workflows, a run by ID and all runs in stable order. Listing is intentionally
unpaginated for this small exercise. Clients poll a single run for progress.

Separate `createRun`, `startRun` and `cancelRun` mutations share the same application
operations as HTTP. The earlier read-only proposal was reversed during plan review
to meet the README requirement that tools start runs through GraphQL. The provided
HTTP routes and JSON/status values remain supported.

The read model includes steps, attempts, execution outcomes and measurement state
(`PENDING`, `STORED`, `LOST`, or `NOT_APPLICABLE`). This lets a scientist see a
physically completed read whose logical step failed because its bytes were lost.
Unknown physical evidence is absent, rather than a fabricated execution/time.
Enums are uppercase; timestamps are UTC RFC3339 strings. Errors are nullable and
execution history is retained. Creation is not idempotent; clients must not blindly
repeat an uncertain create request. Start and cancel are idempotent.

```graphql
mutation Create { createRun { id status } }
mutation Start($id: ID!) { startRun(id: $id) { id status } }
query Follow($id: ID!) {
  run(id: $id) {
    id status cancellationRequested dispatchPaused
    steps {
      name status error dataRef
      attempts {
        jobId number recoveryError
        executions {
          id outcome error
          measurement { state sha256 dataRef error }
        }
      }
    }
  }
}
```

Select an `operationName` when sending the multi-operation document. Poll roughly
once per second, slower during outages/unresolved recovery. Stop normal progress
polling at a terminal run status. Polling returns persisted history/current state;
it cannot promise that the client sees every intermediate transition.

## 4. What was tested and what broke

See [`docs/validation.md`](docs/validation.md) for exact reproduction commands,
component coverage, setup failures, safety regressions and resulting fixes.

All **33 scenarios** passed at application source revision `3df4d08`, verified by
source fingerprint `f6b94cce023d43aca8a25b8d782e76409ef60307dcf49794820872b85617d38b`. Passing results were
retained while the runner was corrected for read-only GraphQL and proven pre-send
connection failures; the remaining scenarios were explicitly resumed. This was a
segmented campaign, not one uninterrupted passing invocation. No application
source or dependency version changed between those segments.

The matrix includes both seven-check acceptance workflows; all executor/driver
partitions and process deaths; storage outage; safe/nonretryable failures;
three-attempt exhaustion; real 30-second hangs; repeated 60-second hangs;
concurrent workflows; pending/active cancellation; GraphQL execution; NATS/Postgres
restarts; overdue unresolved work with later recovery; storage plus driver death;
and random seeds **7, 11, 23, 47**, each for 60 seconds with all created runs drained.

The fail-then-succeed instrument fixtures use **CHAOS_SEED=2** for the incubator
and **CHAOS_SEED=4** for the plate reader, with 50% failure or hang probability.
Persistent failure/hang cases use 100% on the selected device. The longer hang case
uses Triple Assay, HANG_DURATION=60s and RECOVERY_TIMEOUT=75s. Every scenario starts
with explicit fault settings and isolated volumes; final acceptance has faults zero.

Go tests, vet, Python harness tests and containerized Go 1.26 race checks passed.
Combined component/unit tests with real dependencies reported coverage of
69.7% for lab protocol/spool/transport, 63.1% for the driver,
and 69.9% for the executor. These are Go test coverage figures;
the separate Docker fault campaign is not included in those counters. No claim of
100% coverage or mathematical exactly-once execution is made.

See [`docs/validation-results.json`](docs/validation-results.json) for all scenario
results, timing, segment references and retained failed attempts, and
[`docs/validation-evidence/`](docs/validation-evidence/) for selected portable logs
and the three-attempt long-hang ledger. Raw artifacts remain under `artifacts/`.

## 5. Limits and unproven behavior

- The fixed instrument cannot atomically transfer measured bytes into the spool.
  A crash after physical measurement but before capture can irrecoverably lose
  the actual data. The record reports that loss; lossless preservation of every
  physical read is not guaranteed.
- The instrument logs terminal-ledger write failures and can retain newer evidence
  only in memory. Disk corruption or volume loss can leave insufficient evidence;
  it is outside the automatic recovery guarantee and must not authorize unsafe work.
- Recovery assumes intact Postgres, NATS, ledger and spool volumes. There is no
  replicated failover or fencing for multiple active owners.
- Runs can remain running indefinitely when evidence cannot be recovered. There
  is no unsafe force-complete/force-retry endpoint or automatic operator repair.
- Spool records/bytes and attempt history are retained without garbage collection.
  Explicit capacity backpressure is safe but indefinite production operation needs
  retention/archival policy and pagination.
- A logical step's interval can span several physical attempts/backoff periods;
  GraphQL execution intervals are the precise physical history. Current HTTP model
  shapes cannot expose the entire attempt history.

## 6. Time spent

Approximately **97 minutes** elapsed in this implementation/validation session
(16:20–17:56 UTC on 2026-10-24), including builds, fault-test waits, fixes,
and documentation. Earlier user reading/planning time was not supplied and is
not inferred from commit dates, so this is not a claimed total for all exercise work.

## 7. Exercise feedback

Implementation observations: the independent ledger and measurement hashes made
physical/data claims testable. Cancellation and ambiguous execution needed explicit
policy because the fixed HTTP vocabulary has no unknown state. Realistic hangs and
fresh stack startup dominated iteration time; focused component tests and targeted
scenario reruns helped. I would add durable outcome/data retrieval by job ID to the
instrument contract, or explicitly state the expected loss policy for the current
measurement handoff window. Driver death is also useful as a first-class chaos action,
which this implementation adds. No additional personal feedback from the user was
supplied; these are observations from the assisted implementation.

## 8. AI assistance and submission

Planning prompts/responses and decisions are in `ai/`, with a partial implementation
conversation/activity record in `ai/005-implementation-response.md`. Repeated progress
messages and most raw tool output are omitted and that omission is disclosed there.
A complete platform-exported transcript was not generated by the tools. The user
can add an export/share link before submission for a fuller conversation record.
No GitHub publication has been performed by this implementation task.

## 9. Scientist web UI follow-up

The executor now serves a responsive notebook at `/` on its existing port
(`http://localhost:5002/` with Compose). Plain HTML, CSS and JavaScript are embedded
in the Go binary. There is no frontend build, additional service, or runtime CDN.
All workflow and run operations use `/graphql`. Scientists create a pending run,
review it, then explicitly start it; cancellation is a separate action.

The page polls every two seconds, retains expanded attempt details, and displays
logical step status separately from physical executions and measurement state.
It includes recovery errors, cancellation intent, dispatch pauses, timestamps,
job/execution IDs, hashes and S3 references. A failed refresh retains the last
record with a stale-data warning. Mutations are never automatically retried:
a failed response asks the scientist to inspect the record before trying again.
Run selections are bookmarkable. No authentication, data download proxy,
pagination, or new execution/recovery policy was added.

Validation for this follow-up uses Go route tests, eight Node.js behavior tests,
and Chromium against fresh test-owned infrastructure. The browser checks actual
create/start/completion, measurement references, pending cancellation, read outage
and recovery, uncertain mutation handling, missing records, bookmark reload,
browser Back navigation, expanded details and a 390px layout. The runner also compares UI-created records
with independent instrument ledgers and stored data hashes, then runs HTTP
acceptance. It stops at the first failure and removes owned infrastructure and
application image tags. Exact results and reproduction commands are in
[`docs/web-ui-validation.md`](docs/web-ui-validation.md). The earlier 33-scenario
fault campaign was not repeated for this UI-only extension.

The existing development executor was rebuilt as well. This container's workspace
path is not shared with Docker Desktop, so the normal config bind mount was
rejected. A local resolved Compose override omitted only that executor mount,
using the identical config baked into the image. Its `/health` and `/` responded
successfully; existing databases and driver volumes were retained.

This follow-up took approximately 12 minutes, including browser checks, local
Docker setup and documentation, in addition to the earlier implementation time.
The prompt and a partial activity record are in `ai/006-web-ui.md` and
`ai/006-web-ui-response.md`; repeated progress and raw tool output are omitted.
