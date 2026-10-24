# Implementation notes

Status: planning; initial decisions are recorded below. Implementation and
validation results are pending.
Replace the placeholders as work progresses. Record observations separately from
design intentions and assumptions.

## Plan
Use Astra to help create a high level plan. Asking me question on how we should proceed. These are captured in ai/001-plan-response.md

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
| Physical execution and retries | Agreed: bounded exponential retries with recovery spanning the configured hang duration; proposed status-check delays are 0.5, 1, 2, 4, 8, 8, 8, 8 seconds (39.5 seconds total) | Stop after every failure | Recovery checks are separate from physical attempts; physical-attempt limit and retry eligibility remain open |
| Persistence and recovery | TODO | TODO | TODO |
| Cancellation and hangs | Recovery must cover the default 30-second hang; proposed configurable deadline is 45 seconds, extended for longer hangs | Shorter retry windows exhaust before the instrument frees itself | Confirm idle status and terminal ledger evidence before any safe physical retry; cancellation policy remains open |
| Data durability | TODO | TODO | TODO |

Assumptions and interpretation of unclear requirements: TODO

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
