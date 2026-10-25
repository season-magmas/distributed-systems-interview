# Code review

Reviewed the changes from `59aca86` (`Initial import`) through `52ec6ab`, with emphasis on physical execution identity, durable messaging, recovery, cancellation, measurement storage, GraphQL, and the scientist UI. Two actionable findings follow. This review changes documentation only.

## Findings

### 1. [P2] Reject an empty run ID before invoking the list-query path

**Location:** `services/executor/graphql.go:74–82`, with the underlying sentinel handling at `186–224`.

GraphQL's `ID!` accepts an empty string. Consequently, `{ run(id: "") { id status } }` reaches `graphRecords(ctx, "")`, where an empty ID means "list every run." The resolver then returns `records[0]`. With existing runs, this returns the newest run even though its ID does not match the requested ID. With an empty database, it indexes an empty slice and produces a resolver panic/error instead of the normal missing-run response. A client passing an unset identifier can therefore receive an unrelated scientific record.

Validate the ID in the single-run resolver, or separate the single-run and list lookup APIs so user input cannot select the internal sentinel. Add cases for an empty ID with both zero and multiple existing runs; neither should return another run. This finding follows directly from the resolver and SQL branches; it was not exercised against Postgres during this review.

### 2. [P2] Invalidate older detail requests when accepting a newer record

**Location:** `services/executor/web/app.mjs:54–60`; mutation coordination at `103–110`.

`detail()` only checks that the selected run ID still matches. It does not check whether a later read or mutation has already supplied a newer record for that same ID. `act()` waits for `refreshing`, but a detail request started by `select()` is not included in that promise.

A reproducible sequence is:

1. Select a pending run and delay that detail response.
2. Let a background refresh fetch and display the same pending run. Start is now available.
3. Start the run and receive its `RUNNING` mutation response.
4. Deliver the original selection response containing `PENDING`.

The late response overwrites `state.run`, so the UI shows the run as pending and offers Start again after execution has begun. Other out-of-order responses can similarly hide newer cancellation or completion evidence until another successful poll. The backend's idempotent Start handling limits the execution impact, but the displayed scientific record regresses.

Use a detail-request generation/token, invalidate outstanding reads when applying a mutation result, and only let the current generation update the record or its loading state. Add a test controlling promise resolution order across selection, polling, and mutation. I reproduced the sequence above against the exported `createNotebook` using an injected request function: the status was `RUNNING` after `act('start')` and became `PENDING` after resolving the delayed selection.

## Validation and limits

- `go test ./...` passed. External-service integration tests skipped because `LAB_TEST_DATABASE`, `LAB_TEST_NATS`, and `LAB_TEST_S3` were not configured.
- `go test -cover ./...` passed under the same limitations: lab 53.3%, storage 58.1%, driver 50.5%, executor 27.1%. These are this review's local results, not a replacement for the separately recorded integration coverage.
- `node --test services/executor/web/app.test.mjs` passed.
- `python3 -m unittest discover -s scripts/tests` passed all seven tests.
- An inline Node reproduction confirmed finding 2, using the existing application module without modifying it.
- Acceptance, chaos campaigns, browser tests, and external-service integration tests were not rerun. Prior checked-in evidence was read as historical evidence, not counted as newly executed validation.

The explicitly documented single-owner deployment, retained-history capacity limits, and fixed instrument's pre-capture measurement-loss window were treated as declared scope boundaries. They are not additional findings here. This review does not establish correctness under every fault interleaving.

This file captures the review response to `ai/008-code-review.md`; tool output is summarized above rather than included as a verbatim conversation transcript.
