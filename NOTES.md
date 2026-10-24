# Implementation notes

Status: planning; initial decisions are recorded below. Implementation and
validation results are pending.
Replace the placeholders as work progresses. Record observations separately from
design intentions and assumptions.

## Plan
* Use Astra to help create a high level plan. Asking me question on how we should proceed. These are captured in ai/002-plan-response.md.
* Each part was then planned in detailed using a simple Q/A process. These are captured in ai/003-plan-part-X.md.

## 1. Topology

- Executor-to-driver subjects and message payloads: TODO
- Driver-to-executor subjects and message payloads: TODO
- Streams, persistence, retention, and consumers: TODO
- Delivery, acknowledgement, and redelivery rules: TODO
- State ownership (Postgres, NATS, instrument ledger, object storage): TODO
- Job identities, deduplication, and restart reconciliation: TODO
- Data upload and publication of `data_ref`: TODO
- Diagram: TODO

## 2. Decisions and alternatives

| Decision | Choice and rationale | Alternatives considered | Tradeoffs |
| --- | --- | --- | --- |
| Messaging topology | Agreed: JetStream for durable messages and a Postgres outbox for outbound intent | Core NATS with persisted reconciliation | Durable delivery requires deduplication and recovery logic |
| Physical execution and retries | Agreed: bounded exponential retries with recovery spanning the configured hang duration; proposed status-check delays are 0.5, 1, 2, 4, 8, 8, 8, 8 seconds (39.5 seconds total) | Stop after every failure | Three physical attempts total; recovery checks consume none; retry confirmed hangs only on safe devices |
| Persistence and recovery | Agreed: one executor, one driver per device, and durable local driver spool; unresolved work stays running with an explicit recovery error | Active replicas or volatile driver buffers | Process restart recovery depends on intact volumes; see ai/003-plan-part-2.md |
| Cancellation and hangs | Recovery must cover the default 30-second hang; proposed configurable deadline is 45 seconds, extended for longer hangs | Shorter retry windows exhaust before the instrument frees itself | Confirm idle status and terminal ledger evidence before any safe physical retry; cancellation stops new authorization and settles already dispatched work, preserving actual outcomes |
| Schema and delivery | Agreed: recreate disposable databases and implement in small tested commits | Versioned upgrades; one large implementation commit | Existing disposable records are not preserved; see ai/003-plan-part-4.md |
| Data durability | Persist captured bytes locally, then verify stored measurement; irrecoverably lost bytes fail the step without automatic re-read | Re-read after data loss | The fixed instrument cannot guarantee durable handoff of every measurement |

Detailed planning contracts: `ai/003-plan-part-1.md` and
`ai/003-plan-part-2.md`, `ai/003-plan-part-3.md`, and `ai/003-plan-part-4.md`. These describe planned
behavior, not tested guarantees.
Implementation details within the specifications remain subject to review.

## 3. GraphQL interface

- Schema (or link to its source): TODO
- Queries and mutations: TODO; agreed that clients follow runs by polling.
  Polling was chosen to keep the interface simple; subscriptions are unnecessary
  for the initial scope.
- Status, error, nullability, and data-reference semantics: TODO
- Why this shape suits scientists and tools: TODO
- Example operations: TODO

## 4. Validation and fault injection

No implementation checks or fault experiments have been recorded yet.

Agreed test strategy: extend `scripts/chaos.sh` to kill/restart drivers, create
and clean isolated test-owned resources while retaining diagnostics, and provide
a routine suite plus an extended suite. See `ai/003-plan-part-3.md` for scenarios,
independent assertions, timeout rules, and artifact requirements.

Agreed recovery validation: include NATS and Postgres restarts with intact volumes
in the extended suite. Stop after the first failure, preserve diagnostics, clean
owned resources, and mark remaining cases not run. `ai/003-plan-part-5.md` defines
the recovery phases and completion gate.

| Scenario | Exact command, seed, and configuration | Observed API, ledger, and data results | Fix and retest |
| --- | --- | --- | --- |
| Unit tests and coverage | TODO | Not run | TODO |
| Happy-path acceptance | `./scripts/acceptance.sh` | Not run | TODO |
| Executor restart | TODO | Not run | TODO |
| Driver disconnect | TODO | Not run | TODO |
| Storage outage | TODO | Not run | TODO |
| Instrument failure or hang | TODO | Not run | TODO |
| Seeded random chaos | TODO | Not run | TODO |

Record environment details and restored fault settings here: TODO

## 5. Limits and unproven behavior

- Known cases where the record may not be true: TODO
- Behavior not yet tested or proven: TODO
- Intentional boundaries and their rationale: TODO
- Remaining risks and next steps: TODO

## 6. Time spent

Approximate actual total: TODO

| Activity | Time spent |
| --- | --- |
| Reading and design | TODO |
| Implementation | TODO |
| Tests and fault experiments | TODO |
| Documentation | TODO |

## 7. Exercise feedback

- What was unclear: TODO
- What was annoying: TODO
- What I would change: TODO

## 8. AI assistance and submission

- Conversation exports in `ai/` or share links: TODO
- Material omitted from conversations, if any: TODO
- How AI assistance was used and decisions reviewed: TODO

Before submission, replace placeholders, commit this note, record actual time,
and include conversation exports or links. Follow the README submission steps.
