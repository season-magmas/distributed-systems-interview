# Implementation notes

This implements the lab executor/driver exercise using JetStream, a Postgres
outbox, persistent driver spools, and GraphQL queries/mutations with polling.
All 33 recorded system scenarios passed across the retained/resumed campaign
segments described below. Known limits remain explicit.
The detailed design and interview decisions are in `ai/003-plan-part-1.md` through
`ai/003-plan-part-7.md`. Reproduction and failure evidence are in
[`docs/validation.md`](docs/validation.md).

## Personal notes during progress

### Plan
* Use Astra to help create a high level plan. Asking me question on how we should proceed. These are captured in ai/002-plan-response.md.
* Each part was then planned in detailed using a simple Q/A process. These are captured in ai/003-plan-part-X.md.
  * Rejected suggested use of graphql mutations as we already have existing HTTP methods and want to keep things as simple as possible.
    * This was then reverted on plan review

### Implementation
* Use Sol to implement the plan as above.
* Taking a break while Sol gets busy.

### Testing
* Manual testing using provided acceptance.sh and chaos.sh with multiple concurrrent workflows in progress.

### Extension
* Added simple UI to aid testing and verify sane outputs.

---

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

The concrete message routes are:

| Sender → receiver | Subject | Stream | Durable pull consumer |
| --- | --- | --- | --- |
| Executor outbox publisher → liquid-handler driver | `lab.commands.liquid-handler-1` | `LAB_COMMANDS` (`lab.commands.*`) | `liquid-handler-1`, filtered to that exact subject |
| Executor outbox publisher → incubator driver | `lab.commands.incubator-1` | `LAB_COMMANDS` | `incubator-1`, filtered to that exact subject |
| Executor outbox publisher → plate-reader driver | `lab.commands.plate-reader-1` | `LAB_COMMANDS` | `plate-reader-1`, filtered to that exact subject |
| Each driver → executor | `lab.results.<device>` | `LAB_RESULTS` (`lab.results.*`) | `executor`, filtered to `lab.results.*` |

Commands carry run/step/device identity, a stable job ID, attempt number and
deadlines. Reports carry that command identity, a revision, physical executions,
recovery information and measurement disposition. Measurement bytes go through
the driver spool to S3; they are not carried in NATS messages.

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

State ownership is split by responsibility:

| State | Owner and durable location |
| --- | --- |
| Run/step status, cancellation intent, retry scheduling, attempts and device reservations | Executor; Postgres |
| Outbound intent, received-report deduplication and invalid-result evidence | Executor; Postgres outbox, inbox and rejections tables |
| Commands/results awaiting acknowledgement | NATS; persistent JetStream volume |
| Captured measurement bytes, replayable reports and invalid-command evidence | Each driver; its own persistent `/spool` volume |
| Evidence of actual physical work | Fixed instrument; `/ledger/<device>.jsonl` on the persistent ledger volume |
| Verified measurement objects | Object storage; persistent storage volume, referenced as `s3://bucket/key` |
| Workflow definitions for new runs | `config/workflows.yaml`; copied into each run's stored step definitions at creation |

Broker publish acknowledgements and consumer acknowledgements serve different
purposes: a publish acknowledgement permits marking an outbox item published or
advancing a driver's saved report; it does not mean the receiving application has
processed it. The driver's consumer acknowledgement follows terminal report
publication; the executor's follows its database commit. If either consumer ack
is lost, redelivery reuses saved identity/evidence. Invalid commands are saved in
the spool before acknowledgement; invalid results are recorded in Postgres before
acknowledgement. No acknowledgement depends on an HTTP client staying connected.

## 2. Decisions and alternatives

The main choices and their costs are explicit below. Alternatives are design
options considered against the exercise constraints, not additional systems that
were implemented and benchmarked.

| Choice | Alternative | Reason and cost |
| --- | --- | --- |
| Durable JetStream command/result queues | Core NATS request/reply | Disconnected consumers and restarts need replay. This adds stream/consumer administration and duplicate delivery handling. |
| Transactional Postgres outbox | Update the run and publish directly in separate operations | A crash between those operations could lose an authorized command or publish unrecorded work. The outbox adds a publisher loop and publication latency. |
| Driver-local durable spool | Keep outcomes/measurement bytes only in memory, or require drivers to use Postgres | Bytes and reports must survive driver restarts without giving drivers database access. This adds local storage management and cannot close the fixed instrument's pre-capture crash window. |
| One durable consumer and serial driver per device, plus persisted reservations | A shared worker pool or several active drivers for one device | Ownership follows the physical one-job-at-a-time constraint. This simplifies safety but provides no active-owner failover or distributed fencing. |
| Work-queue retention with no age expiry and reject-new capacity limits | Expire/evict older messages or retain an unbounded event log | Unprocessed intent/evidence must not silently disappear. Backpressure is explicit, but a full queue can block progress and acknowledged messages are not a permanent audit log; Postgres/spools retain that history. |
| Run/step JSON aggregate plus relational attempts/reservations/outbox | Separate normalized run and step tables, or an in-memory scheduler | One aggregate simplifies consistent existing HTTP reads; transactional tables enforce scheduling identities. Aggregate rewrites and a global transition lock limit scale. |
| Preserve unresolved work as running with paused dispatch | Treat timeout as failure and start replacement work | Ending a wait does not stop physical work. This avoids unsafe repeats but can require indefinite waiting or operator investigation. |
| Three evidence-backed attempts, with separate recovery polling | Unlimited retries or retry immediately on a timeout/busy reply | Bound physical work and honor instrument retryability. Transient faults can exhaust the budget; observation retries do not count as physical attempts. |
| Verified original measurement bytes and explicit data loss | Re-read the plate or trust an upload response without verifying data | A later read is a different physical measurement. Hash verification adds storage round trips and data loss can fail a logically required step after physical completion. |
| Cancellation as persisted intent | Immediately label all active work cancelled | The instrument continues working after its context ends. Actual outcomes must still be recorded, so cancellation is not immediate physical abort. |


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

The complete schema, including argument types, enums and nullability, is in
[`docs/graphql-schema.graphql`](docs/graphql-schema.graphql). It documents the
programmatic schema rather than introducing a second schema implementation.
A test compares its object fields, arguments and enums with the running schema
construction to keep the documentation accurate.

The schema follows the questions a scientist needs to answer:

| Shape or choice | Why it is shaped this way; alternative and cost |
| --- | --- |
| `Run → Step → Attempt → Execution → Measurement` | Separate the requested workflow, each authorized job and actual physical evidence. A flat step status would hide retries, duplicate executions or a completed read with lost data. The deeper model requires clients to select the detail they need. |
| `workflows`, `runs`, and nullable `run(id)` | Discover valid workflow names, browse history, then poll one selected record. A missing ID returns null; an empty list means no records. Lists are unpaginated for this bounded exercise, trading future scale for a simple client. |
| Separate `createRun`, `startRun`, `cancelRun`, returning a run | Review a pending run before starting equipment and expose cancellation intent explicitly. A combined create-and-start mutation would remove that review point. Shared HTTP application operations avoid divergent execution rules. |
| `cancellationRequested` and `dispatchPaused` alongside run status | Show intent and unresolved evidence without inventing a new public HTTP status or falsely declaring physical work stopped. A single status field could not convey both dimensions. |
| Physical `outcome` separate from measurement `state`, hash and reference | Scientists need to distinguish completed physical work from usable stored data. A nullable URI alone would not distinguish pending upload from confirmed loss. `measurement` is null for non-reader executions. |
| Non-null known identity/status/list fields; nullable evidence fields | IDs and stored status are known; finish times, errors and measurements may not exist yet. Empty attempt/execution lists mean no such evidence is recorded, not proof that no physical work occurred. Synthetic timestamps or placeholder executions would overstate knowledge. |
| Enums, UTC RFC3339 timestamp strings and ID scalars | Give clients stable vocabulary and portable timestamps without extra custom scalar machinery. Strings do not provide schema-level date validation; local formatting is the client's responsibility. |
| Polling over ordinary POST requests | The exercise needs current persisted records, not every transient event. Subscriptions would add connection/reconnect and event-delivery state. Polling is simpler but adds read load, a short display delay and no guarantee of observing every intermediate transition. |

For example, a reader execution can have `outcome: COMPLETED` while its
measurement is `LOST` and the logical step is `FAILED`. These are different facts,
not conflicting statuses. `retryable` reflects the instrument's assessment for
failed/hung work; null on other outcomes does not grant permission to retry.

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

The following are observed results from that campaign, not new fault runs made
while writing this note. The commands recreate disposable test-owned stacks; the
runner waits for active physical work before injecting process/network faults.

| Command / injected fault | Settings | What was observed |
| --- | --- | --- |
| `./scripts/system-test.sh --scenario kill:plate-reader-1` | Kill/restart the reader driver during an active read; instrument faults otherwise zero | The ledger retained one hung read and one later completed read. The run completed and the final stored measurement matched its execution. This validated the confirmed-hang retry policy. |
| `./scripts/system-test.sh --scenario storage` | Stop storage for 8 seconds during a read | The run completed with exactly one physical reader execution; upload recovery did not re-read the plate. |
| `./scripts/system-test.sh --scenario storage-driver` | Same storage outage, plus kill/restart the reader driver after 4 seconds | The run completed with one physical read, validating recovery of captured bytes across a driver restart. |
| `./scripts/system-test.sh --scenario failure:liquid-handler-1` | `FAIL_PCT=100`, `CHAOS_SEED=0` on the liquid handler | One failed liquid-handler execution; the run failed, no repeat dispensing occurred, and dependent steps did not execute. |
| `./scripts/system-test.sh --scenario hang:incubator-1:long` | Triple Assay; incubator `HANG_PCT=100`, `CHAOS_SEED=0`, `HANG_DURATION=60s`; executor `RECOVERY_TIMEOUT=75s` | Exactly three incubator executions became hung; the run failed after exhausting its safe attempt budget. The entire scenario, including setup, took 228.33 seconds. |
| `./scripts/system-test.sh --scenario unresolved` | Disconnect the liquid-handler driver for 55 seconds; inspect the record after 47 seconds | The run remained running with an explicit step error while evidence was unavailable. After reconnection it completed with four liquid-handler, two incubator and one reader execution, all completed. |
| `./scripts/system-test.sh --scenario random:7` (also `random:11`, `random:23`, `random:47`) | Each seed injects faults for 60 seconds under finite concurrent load | Respectively 3, 4, 4 and 2 runs were created and drained; all 13 completed. Ledger checks found no duplicate job execution or device overlap, and measurement hashes matched. This samples fault schedules; it is not exhaustive. |

The corresponding settled run IDs, per-device outcomes, fault settings and the
intermediate unresolved snapshot are captured in
[`docs/validation-evidence/fault-observations.json`](docs/validation-evidence/fault-observations.json),
extracted from the retained raw artifacts. Both complete acceptance workflows
also passed their seven independent checks; their output is committed under
`docs/validation-evidence/`.

Several failures did require changes, rather than merely confirming a policy:

| Failure observed during implementation | What changed because of it |
| --- | --- |
| A synthetic terminal reader report claimed stored data without a hash/reference; the executor could accept unproven success | Reject and durably record that report before it can complete the step or unlock dependents. Covered by `TestIntegrationRejectUnprovenSuccess` and the dependent-dispatch gate test. |
| Recovery deadlines did not grow with longer configured work/hangs | Derive the driver's recovery window from at least `max(normal duration, hang duration) + 15s`; retain bounded observation backoff. Unit coverage checks the duration boundary; the real 60-second hang case above exercises the result. |
| Recovering an already verified object still attempted another PUT; a failed redundant write could also produce an empty reference | Recover the S3 reference from the verified existing object without rewriting it. Covered by `TestWorkerRecoversVerifiedObjectWithoutAnotherWrite`. |
| A recovered terminal run could keep an old uncertainty pause | Clear resolved uncertainty when settling terminal evidence. Covered by `TestIntegrationLateEvidenceClearsUncertainty`. |
| Missing ledger evidence could allow a spool's existing physical evidence to be replaced | Preserve the spool and report the conflict without authorizing replacement work. Covered by `TestWorkerSpoolEvidencePreventsReexecutionWithMissingLedger`. |
| Postgres readiness could detect its temporary init server, and restarted containers could receive different host ports | Fix the test harness: TCP readiness and stable assigned ports. These were harness defects, not instrument faults. |
| The first two final-campaign invocations stopped on client connection/readiness errors | Fix bounded read retries and retry only provable pre-send connection failures; never replay a sent mutation automatically. Retain the passing prefix and explicitly resume remaining scenarios, as recorded above. |

Focused crash-window tests complement the Docker campaign. For example,
`TestWorkerLostBytesDoesNotReread` deliberately discards the returned bytes after
physical measurement, then checks for `COMPLETED` physical evidence, `LOST` data,
and exactly one execution. That simulates the handoff gap; it is not an observed
OS kill at a precisely timed instruction. `TestIntegrationOutboxFailureRollsBackAuthorization`
injects a database trigger failure and checks that authorization rolls back with
the failed outbox insertion. Reproduce these focused checks with:

```sh
go test ./services/driver -run 'TestWorker(LostBytesDoesNotReread|RecoversVerifiedObjectWithoutAnotherWrite|SpoolEvidencePreventsReexecutionWithMissingLedger)' -count=1
./scripts/system-test.sh --scenario integration
```

## 5. Limits and unproven behavior

The boundaries have different causes; they are not all claims of completed
reliability work:

| Boundary | Was it deliberate, and what remains unproven? |
| --- | --- |
| Physical measurement → durable spool handoff | A limitation of the fixed instrument interface. Reporting confirmed loss instead of silently re-reading is deliberate; lossless capture is not achieved. Focused tests simulate the missing-byte window, rather than proving every process-kill timing. |
| One executor and one driver per device | Deliberate scope agreed during planning. Durable reservations/local locks protect this deployment; safe multiple active owners, distributed fencing and automatic HA failover were not implemented or tested. |
| Intact persistent volumes and normal filesystem durability | Recovery is conditional on these assumptions. Process restarts were tested; actual host power loss, faulty storage, lost volumes and disaster recovery were not established. Sync/rename and corruption checks do not prove survival under all storage failures. |
| Indefinitely unresolved work | Deliberate preference for truthful uncertainty over unsafe replacement execution. Late evidence recovery was tested; guaranteed eventual completion and an operator repair procedure were not delivered. |
| Finite capacity, retained history and unpaginated queries | Deliberate exercise simplifications. Spool-capacity refusal has focused coverage, but long-running production retention, sustained overload and large-history performance were not demonstrated. |
| HTTP status vocabulary and logical step intervals | Deliberate compatibility boundary. A logical interval can include retries/backoff; GraphQL exposes individual physical executions. The existing HTTP shape cannot express every evidence distinction. |
| Finite fault campaign and separate UI validation | A practical testing boundary. The 33 cases cover selected schedules at the recorded pre-UI source; later UI browser/acceptance checks are separate. Neither result proves all interleavings, every future change, or mathematical exactly-once execution. |

The intended guarantee is a conservative, evidence-based record for the agreed
single-owner deployment and intact storage, exercised by the recorded tests. It
is not a guarantee that every run finishes or every physical measurement survives.
Where evidence is insufficient, the implementation must preserve uncertainty
instead of inventing a successful outcome or authorizing unsafe repetition.

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

Personal time was approximately **3 hours**, excluding the time when the AI-assisted
implementation was taking place. The implementation/validation session took
approximately **97 minutes** (16:20–17:56 UTC on 2026-10-24), including builds,
fault-test waits, fixes and documentation. The web UI follow-up took approximately
**12 minutes**, and the final submission review took a few additional minutes.
Together these recorded periods amount to roughly **5 hours overall**, with
personal time and assisted implementation time reported separately.

## 7. Exercise feedback

Personal feedback: it would have been helpful to have a known GitHub username
with whom to share the project when submitting it.

Implementation observations: the independent ledger and measurement hashes made
physical/data claims testable. Cancellation and ambiguous execution needed explicit
policy because the fixed HTTP vocabulary has no unknown state. Realistic hangs and
fresh stack startup dominated iteration time; focused component tests and targeted
scenario reruns helped. I would add durable outcome/data retrieval by job ID to the
instrument contract, or explicitly state the expected loss policy for the current
measurement handoff window. Driver death is also useful as a first-class chaos action,
which this implementation adds. These implementation observations are separate
from the personal feedback above.

## 8. AI assistance and submission

Planning prompts/responses and decisions are in `ai/`, with a partial implementation
conversation/activity record in `ai/005-implementation-response.md`. Repeated progress
messages and most raw tool output are omitted and that omission is disclosed there.
A complete platform-exported transcript was not generated by the tools. The user
reviewed the existing AI records and considers them a complete functional picture
of what took place; these are the records selected for submission, with the stated
omissions retained. No GitHub publication has been performed by this implementation
task.

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

## 10. Submission review

[`docs/submission-review.md`](docs/submission-review.md) maps README requirements
to implementation and evidence. Current Go tests/vet, Node.js UI tests and Python
harness tests passed again; the historical fault/browser campaigns were reviewed,
not rerun. Fixed files and public Go parameter/result types were checked against
the original import. The review took a few additional minutes.

The user supplied personal time and exercise feedback, now recorded in sections
6–7, and confirmed that the existing AI records provide a complete functional
picture. The disclosure of omitted progress/tool output remains. This review and
the user's answers are recorded in `ai/007-finish-response.md`. The README write-up
requirements are complete; publishing the repository and sending its link remain
the user's submission steps.

## 11. Post-review fixes (2026-10-25)

Both findings in `ai/008-code-review-response.md` are addressed. An empty GraphQL
run ID now returns `run: null` before it can enter the internal list-all lookup.
Regression tests cover an absent database dependency and real Postgres with zero
and two existing runs, while confirming that the ordinary `runs` query still
lists all records.

The notebook now assigns generations to detail requests and invalidates older
reads before mutations. Only the current request can update the record, read
error, or loading flag. Controlled promise tests cover delayed selection versus
polling/start/cancellation, obsolete failures, revisiting the same run, uncertain
mutation responses, and recovery from a current read failure. The original empty
ID test and three request-order tests failed before the fixes and passed after.

Reused device IDs, the executor consumer name, physical outcomes, and the
measurement-loss code now use named Go constants. GraphQL reuses the existing
run/step status constants. SQL statements remain inline, as the user clarified;
the fixed instrument implementation and public API contracts are unchanged.

Validation commands and observed results:

- `go test ./...`, `go test -cover ./...`, and `go vet ./...` passed. Without
  external endpoints, coverage was lab 53.3%, storage 58.1%, driver 50.5%, and
  executor 27.6%; integration tests skip in these commands.
- `node --test services/executor/web/app.test.mjs` passed all 13 tests.
- `python3 -m unittest discover -s scripts/tests` passed all seven tests.
- `./scripts/system-test.sh --scenario integration` passed against disposable
  Postgres, NATS and object storage. Final-source coverage was lab 69.7%, driver
  63.1%, and executor 70.2%. This includes the empty-ID integration regression.
- `LAB_PLAYWRIGHT_MODULE=file:///tmp/lab-ui-browser/node_modules/playwright-core/index.mjs CHROMIUM_PATH=/usr/bin/chromium python3 scripts/web_ui_test.py`
  passed Chromium checks, independent ledger/hash checks and HTTP acceptance
  (all seven acceptance checks). The browser used the final UI fix; its build
  preceded reverting the equivalent SQL-constant extraction. Final integration
  and executor-kill validation ran after that reversion.
- `./scripts/system-test.sh --scenario kill:executor` passed with seed `0` and
  all instrument fault percentages `0`: the executor was killed during an active
  liquid-handler execution, restarted, and the run completed with consistent
  HTTP/GraphQL records, unique physical job identities and verified reader data.
- `go test -race ./services/common/lab ./services/driver ./services/executor`
  could not run with cgo disabled. Retrying with `CGO_ENABLED=1` failed because
  this environment lacks `gcc`. No race-test pass is claimed.
- Changed Go files were formatted with `go fmt`; `git diff --check` passed.

Portable results are in [`docs/validation-evidence/post-review/`](docs/validation-evidence/post-review/).
Raw logs are in ignored artifacts `lab-test-20261025t055911z-4564` (browser),
`lab-test-20261025t060036z-10629` (final integration), and
`lab-test-20261025t060035z-10545` (executor kill). All three runners reported
successful cleanup of their disposable resources. Acceptance used zero fault
percentages; existing development volumes were retained. The earlier full chaos
campaign was not repeated.

This follow-up took approximately five minutes (05:56–06:02 UTC), in addition to
the earlier sessions. The request and partial response record are in
`ai/009-code-post-review.md` and `ai/009-code-post-review-response.md`; raw tool
output and repeated progress messages are omitted under the existing disclosure.
