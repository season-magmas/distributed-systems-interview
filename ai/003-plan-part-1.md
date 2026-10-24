# Part 1: execution record and recovery contract

Status: specification for implementation planning, 2026-10-24. No behavior has
been implemented or tested. This expands step 1 of `ai/002-plan-response.md`.

## Decisions from the interview

The user's “Yes” accepts the recommended answers to the three part 1 questions:

- Allow three total physical attempts per logical step: the initial attempt and
  at most two retries. Status checks and message redeliveries do not consume this
  budget.
- After a confirmed hang ends, retry incubator and plate-reader jobs when the
  instrument is idle. Fail liquid-handler jobs without repeating a dispense.
- Cancellation stops new work and waits for already dispatched work to settle,
  preserving its actual outcomes.
- If a plate read physically completed but its bytes were irrecoverably lost,
  fail the logical step with an explicit data-loss reason. Do not automatically
  re-read or reconstruct bytes from the simulator's measurement algorithm.

Previously agreed: JetStream with a Postgres outbox, bounded exponential delay,
recovery lasting beyond the configured hang duration, and GraphQL polling.

The user subsequently confirmed D7: unresolved work stays running, with an
explicit recovery error and further dispatch paused until evidence arrives.
Other detailed status mappings and race rules below specify the proposed design.

## Identities and evidence

A logical step is identified by run ID and step name. Each authorized physical
attempt has a distinct job ID and increasing attempt number. Redelivery retains
that identity; it never creates an additional physical attempt. An instrument
execution ID identifies the actual ledger entry and is recorded against its job.

Record every observed execution, including earlier failed attempts. A message
receipt, publish acknowledgement, or timeout is not proof of physical execution.
The driver consults `Status()` and the ledger before deciding whether to start,
resume waiting, report a result, or request another attempt.

An `ErrBusy` response does not count as execution. A dispatch whose execution is
unknown stays associated with its existing attempt; it cannot free the budget
for a speculative replacement. Duplicate executions for one job are a correctness
violation to expose, not records to silently collapse.

## Invariants

1. Only one physical execution occupies a device at a time across all runs.
2. Dependencies must have completed successfully, including required data
   persistence, before dependent work is authorized.
3. Logical completion requires evidence of physical completion. For a data
   producer it additionally requires durable bytes matching that execution's hash.
4. A failed or hung liquid-handler attempt is never automatically repeated.
5. Neither a timeout nor device idleness alone makes another attempt safe.
6. Cancellation and run failure preserve all actual execution outcomes.
7. A retry cannot erase a preceding attempt, its error, or its measured data.
8. Timestamps describe physical evidence; do not substitute dispatch or message
   arrival times for instrument execution times.

## Step transition contract

Internal phases can be richer than the fixed HTTP status vocabulary. The names
below describe semantics rather than prescribing database enum names.

| Situation | Required action | Proposed HTTP step status |
| --- | --- | --- |
| Waiting for dependencies, never dispatched | Leave eligible for scheduling | `pending` |
| Authorized dispatch, physical start not yet known | Reconcile this job; no replacement | `running`, with null physical timestamps until known |
| Instrument executing or recovering from a hang | Observe the existing attempt | `running` |
| Retryable terminal outcome, budget available | Persist retry eligibility and due time | `running` during backoff |
| Physical success, upload still pending | Persist/retry the original bytes | `running` |
| Physical success and required data durably verified | Finish logical step | `completed` |
| Nonretryable outcome or three attempts exhausted | Record explicit reason; stop this step | `failed` |
| Physical read succeeded but bytes irrecoverably lost | Retain successful physical outcome and explicit data-loss error | `failed` |
| Execution cannot yet be determined | Halt new automatic work; retain uncertainty | `running` with an explicit recovery error |
| Cancellation prevents dispatch | Confirm no dispatch is outstanding | `cancelled` with null physical timestamps |

For retrying steps, `started_at` is the first physical start and `finished_at` is
the last terminal physical finish once the logical step settles. The interval can
include retry waits; expose individual execution intervals in GraphQL. If the
physical read finished but upload is pending, its known physical finish may be
reported while the logical status remains running. Do not invent finish times for
unresolved work. `data_ref` is exposed only after verified storage.

## Retry and hang recovery

Physical retries are allowed for confirmed retryable failures and confirmed hangs
on retryable devices. Check device idleness and the previous job's terminal ledger
entry before authorizing a new attempt. On restart, the fixed instrument records
interrupted work as hung; apply the same device safety policy.

Use exponential physical retry backoff starting at 0.5 seconds, doubling to an
8-second cap. With three total attempts, the two physical retry delays are 0.5
and 1 second, measured from confirmation of the preceding terminal outcome.
Persist attempt count and due time so restarting a service cannot reset the limit.
Cancellation or run failure suppresses new physical retries.

Recovery/status observation has a separate schedule: check immediately, then
after delays of 0.5, 1, 2, 4, 8, 8, 8, 8 seconds. The last scheduled check is
39.5 seconds after observation starts, beyond the default 30-second hang.
Continue at the cap within the configured deadline, including a final bounded
check if needed. Proposed recovery timeout is 45 seconds, measured from the
physical start recorded in the ledger; persist that deadline and do not reset it
on restart. Use `max(STEP_DURATION, HANG_DURATION) + 15 seconds` as the minimum
physical recovery timeout (45 seconds with the supplied defaults). Delayed dispatch
must not consume this physical execution window. Separately, persist a dispatch
deadline at authorization (proposed 45 seconds) to detect missing start evidence.
A dispatch deadline is an alert/pause boundary, not an expiry or permission to
replace a command that may still execute. Part 2 defines the two watchdogs.
Bound individual status operations by the remaining deadline.

When an attempt is known to be actively executing, continue observing it without
calling `Run` again. A hung call never returns normally, even when the instrument
frees itself; the ledger observation must release the adapter's wait and allow
recovery to proceed. Ending the wait does not mean stopping physical work.

Deadline exhaustion stops automatic dispatch/retry for affected work and records
an unresolved reason. It must not claim physical failure or cancellation. Late
evidence can still update the record; any continued observation must be rate
limited. Operator resolution and the exact background reconciliation mechanism
are deferred to part 2. This boundary may leave a run nonterminal during a long
partition, rather than reporting an invented outcome.

## Cancellation and run settlement

Persist cancellation intent atomically with preventing further scheduling.
Repeated cancellation requests are idempotent. Previously terminal runs retain
their result. Before any dispatch, cancellation can immediately settle the run.

For the initial design, an outbound command committed before cancellation is
already authorized and may execute. Treat it as outstanding until a terminal
result or positive evidence of suppression arrives; an empty current ledger alone
does not prove that a queued command can no longer arrive. Part 2 must preserve
this rule across outbox publication and broker redelivery.

If cancellation or another step's failure arrives during retry backoff, remove
retry eligibility and settle this already-executed step as failed with its last
physical outcome and an explicit retry-suppressed reason. It has no outstanding
command to wait for. Only never-executed steps with no committed dispatch become
cancelled. Apply this rule atomically with run intent and retry authorization.

Never mark active steps cancelled just because the user requested cancellation.
Keep their actual success, failure, or data-loss outcome. Cancel undispatched
steps and suppress retries. Wait for outstanding commands, physical work, and
required data handling to settle before marking the run cancelled.

Proposed run aggregation: all steps successful means completed unless a prior
cancellation intent won the transaction race; accepted cancellation settles as
cancelled once outstanding work is accounted for. Without cancellation, an
unrecoverable step failure stops new dispatches and retries, drains active work,
and settles the run as failed. Unstarted blocked steps are cancelled with an
explanatory reason. Unresolved physical work keeps the run nonterminal. Run
finish time represents logical settlement, not necessarily the last physical finish.

## Measurement durability

Persist received bytes before relying on them for a result. Upload retries reuse
the same bytes and stable object identity; they do not re-run the instrument.
Associate `data_ref`, execution ID, job ID, and SHA-256 so the record identifies
the exact measurement. A temporarily unavailable store is not proof of data loss.
An integrity conflict or missing/corrupt evidence that cannot be reconciled keeps
the step running with an explicit error and dispatch paused; it is not proof that
the physical execution failed. An established loss of the actual measurement is
the distinct terminal data-loss case below.

If the ledger says a read completed, first reconcile the matching durable spool
and object. Missing bytes in all recoverable locations establish data loss only
after those locations can be checked. On irrecoverable loss, keep the successful
physical execution visible and fail the logical step with a distinct error such
as `measurement_data_lost`. Do not fabricate data or silently select a new read.

## Required verification

- Prove duplicate messages and lost replies cannot create extra executions.
- Prove retryable failures stop after three physical attempts; busy responses
  and status checks consume none of that budget.
- Exercise a 30-second hang and a longer configured hang. Verify no execution
  overlap, safe-device retry after release, and no liquid-handler repeat.
- Test persistent attempt limits and both dispatch/physical deadlines across
  process restarts, including dispatch delayed longer than a hang duration.
- Test cancellation before dispatch, after outbox commit, during execution,
  during backoff, and during upload, including duplicate requests.
- Test successful measurement with delayed upload separately from irrecoverable
  byte loss; verify hashes and physical outcomes in both cases.
- Test recovery deadline expiry and later evidence without false terminal claims.
- Verify every physical execution is represented, dependencies obey successful
  completion, and separate devices can make progress concurrently.

These are requirements for later tests, not claims that checks have passed.
Part 2 will specify durable state, messages, acknowledgement boundaries, and
the recovery mechanisms that enforce this contract.
