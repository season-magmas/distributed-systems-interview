# Part 6: GraphQL run record and polling interface

Status: detailed specification for implementation planning, 2026-10-24.
No GraphQL implementation or runtime validation has been performed.

## Agreed decisions

- Expose attempt history and measurement data state alongside logical run/step
  statuses so a successful physical read and lost bytes remain distinguishable.
- GraphQL is read-only. Create, start, and cancel runs through the existing HTTP
  API. This supersedes the earlier choice to duplicate those operations as mutations.
- Return an unpaginated run list. No cursors, connection types, filters, or extra
  list controls are required initially.
- Clients poll a run by ID for progress. Subscriptions are outside the scope.

Serve `/graphql` on the executor's existing HTTP server. Preserve all existing
HTTP paths, Go signatures, JSON fields, and status values. GraphQL adds a richer
read model; it does not replace `RunView` or `StepView`.

## Proposed schema

Use ordinary strings for timestamps, documented as UTC RFC3339 with optional
fractional seconds. This avoids a custom scalar for the initial interface.
GraphQL enum names are uppercase; map them to the existing lowercase HTTP values.

```graphql
enum RunStatus { PENDING RUNNING COMPLETED FAILED CANCELLED }
enum StepStatus { PENDING RUNNING COMPLETED FAILED CANCELLED }
enum PhysicalOutcome { RUNNING COMPLETED FAILED HUNG }
enum DataState { NOT_APPLICABLE PENDING STORED LOST }

type Query {
  workflows: [Workflow!]!
  runs: [Run!]!
  run(id: ID!): Run
}

type Workflow {
  name: String!
  isDefault: Boolean!
  steps: [WorkflowStep!]!
}

type WorkflowStep {
  name: String!
  deviceId: ID!
  dependsOn: [String!]!
}

type Run {
  id: ID!
  workflowName: String!
  status: RunStatus!
  createdAt: String!
  startedAt: String
  finishedAt: String
  cancellationRequested: Boolean!
  dispatchPaused: Boolean!
  steps: [Step!]!
}

type Step {
  name: String!
  deviceId: ID!
  dependsOn: [String!]!
  status: StepStatus!
  startedAt: String
  finishedAt: String
  error: String
  dataRef: String
  attempts: [Attempt!]!
}

type Attempt {
  jobId: ID!
  number: Int!
  authorizedAt: String!
  recoveryDeadline: String!
  recoveryError: String
  executions: [Execution!]!
}

type Execution {
  id: ID!
  outcome: PhysicalOutcome!
  startedAt: String!
  finishedAt: String
  error: String
  retryable: Boolean
  measurement: Measurement
}

type Measurement {
  state: DataState!
  sha256: String
  dataRef: String
  error: String
}
```

The nested execution list is intentional: an authorized attempt may not yet have
physically started, and an unexpected duplicate execution must remain visible.
A normal attempt has zero or one execution. More than one is a reported invariant
violation; GraphQL must not hide it by returning only a selected execution.

## Read semantics and nullability

- `workflows` reads current workflow configuration, including device assignments
  and dependencies. Existing runs retain their stored workflow snapshot.
- `runs` returns every run, newest first by creation time with ID as a deterministic
  tie-breaker. It does not silently truncate results. An empty database returns `[]`.
- `run(id)` returns null for an unknown ID, without inventing an empty run. Database
  unavailability produces a GraphQL error, not a not-found response.
- Step order follows the workflow snapshot. Attempts sort by attempt number;
  executions sort by physical start time and execution ID. Empty lists are `[]`,
  never null.
- Missing physical evidence yields an empty execution list. Do not synthesize a
  `RUNNING` execution merely because an attempt was authorized.
- Physical start/finish and logical run/step timestamps obey part 1. Nullable
  timestamps indicate absence of evidence, not a zero time.
- `retryable` is null while retryability is unknown or not applicable, and is
  populated for known failure/hang outcomes using the agreed device policy.
  True means the physical outcome permits retry; it does not override cancellation,
  exhausted attempt budget, or the requirement that the device be idle.
- `measurement` is null on non-data-producing instruments. On a plate-reader
  execution, it carries the data state: pending while successful bytes are not yet
  durably verified, stored when verified, lost when absence is established after
  success, and not applicable for terminal executions that produced no measurement.
  A running read can be pending without yet having a hash.
- `sha256` identifies the ledger's measurement when known. `dataRef` is non-null
  only for verified stored bytes. Lost data retains its known hash and explicit
  error. Storage unavailability remains pending, never inferred as lost.
- `Step.dataRef` identifies the successful selected execution's stored measurement.
  Execution-level references preserve the history of every observed measurement.

For example, a read whose bytes were irrecoverably lost has `Step.status = FAILED`,
an execution with `outcome = COMPLETED`, and measurement state `LOST`. This exposes
the physical truth and the failed scientific result without conflating them.

Unresolved work remains `RUNNING`, with `recoveryError` and `dispatchPaused` making
the reason visible. An accepted cancellation can likewise return a running record
with `cancellationRequested = true` while authorized work settles.

## Existing HTTP operations

Tools use the existing HTTP API to control runs, then read the richer record
through GraphQL. This combination satisfies run creation and progress tracking
without a second write interface.

| Operation | Existing endpoint |
| --- | --- |
| Create a run | `POST /runs`, with optional `workflow_name` in the JSON body |
| Start a run | `POST /runs/{id}/start` |
| Request cancellation | `POST /runs/{id}/cancel` |

Preserve existing HTTP behavior and the part 1 cancellation/recovery contract.
Read the returned run ID after creation and use it for GraphQL polling. An accepted
cancellation may still appear running while authorized work settles.

The GraphQL schema has no mutation root. Reject mutation operations through schema
validation. Queries never authorize work or change run state. Backend read failures
produce GraphQL errors; instrument failures remain fields in the returned record.
Keep internal SQL, filesystem paths, and dependency details in server logs.

## Transport and resolver implementation

Support JSON POST requests with `query`, optional `variables`, and `operationName`.
No WebSocket transport, interactive UI, or request batching
is required. Retain normal schema introspection for tools and tests.

Use a Go GraphQL library for parsing, validation, and execution; do not handwrite
a GraphQL parser. Select and pin a compatible library during implementation after
checking its official documentation and support for this small schema, contextual
cancellation, and standard errors. The library is an implementation choice, not
an additional product feature or a reason to add subscriptions/code generation.

Distinguish invalid HTTP/JSON requests from validated GraphQL execution errors.
Document and test the chosen handler's status codes and GraphQL error envelope.
Queries must never mutate the run record. Keep request cancellation and database
timeouts effective through resolver calls.

Load each run's steps, attempts, and executions from a consistent database snapshot
so one response cannot mix a completed step with older pending evidence.
GraphQL queries share a consistent read-model builder. Avoid one database lookup
per field/attempt: batch the nested records for requested runs. Do not introduce
a generic caching layer; polling should observe newly committed results.

## Polling examples

```graphql
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

Clients can poll `Follow` about once per second during ordinary work and slow down
when a run is unresolved or the service is unavailable. Stop ordinary progress
polling when the run is completed, failed, or cancelled. Polling provides current
state and persisted attempt history, not a guarantee of seeing every intermediate
state transition. Use `operationName` when sending a document with multiple named
queries.

## Tests and completion gate

Test the real GraphQL handler and resolver/database integration:

- Schema validation, variables, enums, nullability, unknown IDs, empty lists, and
  operation selection; mutation requests are rejected without changing state.
- Workflow defaults and changed configuration versus existing run snapshots.
- HTTP create/start/cancel actions reflected correctly in GraphQL, including
  repeated start/cancel and cancellation races.
- Attempts with no physical execution, successful retries, duplicate evidence,
  unresolved recovery, pending uploads, and irrecoverable measurement loss.
- Consistent snapshots and agreement between HTTP and GraphQL for common fields;
  HTTP/GraphQL observations at different times may legitimately show progress.
- Real HTTP create/start followed by GraphQL polling to completion, and HTTP
  cancellation observed through GraphQL, including hash comparison with independent
  instrument evidence.
- Unpaginated listing returns all created runs in stable order without truncation.

The interface is complete when those checks pass and the original HTTP acceptance
still passes. Document that unpaginated listing is intended for this exercise's
small dataset; a large retained history may need pagination later. No latency or
scale guarantee is claimed before measurement. Record schema location, examples,
rationale, actual tests, and remaining limitations in `NOTES.md`.
