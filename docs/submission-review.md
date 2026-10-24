# README submission review

Reviewed on 2026-10-24 against the README, implementation, committed evidence,
and the original import (`995c6b6`). This is a requirements/evidence audit,
not a proof of correctness for every possible fault.

| README requirement | Assessment and evidence |
| --- | --- |
| Robust executor/driver communication over NATS | Implemented with durable JetStream commands/results, Postgres outbox, deduplication, stable job identities, device reservations and persistent driver spools. See NOTES sections 1–2. |
| Truthful physical execution and data records during faults | Exercised against independent instrument ledgers and downloaded measurement hashes. Unresolved work stays running/paused, confirmed unsafe liquid-handler failures are not repeated, and lost measurement data is explicit. Pre-spool measurement loss remains possible; see NOTES section 5. |
| Use chaos beyond happy-path acceptance | Recorded 33 passing scenarios across three application-identical campaign segments, including process deaths, partitions, storage outages, failures/hangs, restarts and random seeds 7/11/23/47. See validation.md and validation-results.json. This was not one uninterrupted suite run. |
| GraphQL at `/graphql` on the executor port | Implemented queries plus separate create/start/cancel mutations, with polling and introspection. Real-service GraphQL tests and browser-created runs passed. |
| Preserve HTTP API and public methods/models | Existing routes/contracts retained; acceptance passed for both workflows. Required Executor and Serve signatures match the original import. models.go is unchanged. |
| Do not change the instrument | instrument.go is unchanged from the original import. |
| Data references use `s3://bucket/key` | Implemented and exercised by acceptance and browser/ledger/hash checks. |
| Only executor uses Postgres; one job per physical device | Driver/common sources do not import Postgres clients. Serial adapters and executor reservations enforce the agreed single-owner deployment; ledger checks cover device overlap. Multiple active owners/HA are outside the design. |
| NOTES topology and reasoning | Sections 1–2 document subjects, streams, consumers, acknowledgements, state ownership, choices and alternatives. |
| NOTES GraphQL schema and reasoning | Section 3 describes the read model, mutations, nullability, timestamps, enums and polling. |
| NOTES faults, results and fixes | Section 4 plus committed validation records give commands, settings, seeds, failures and fixes. |
| NOTES remaining limits | Section 5 explicitly covers measurement handoff, volume loss/corruption, single ownership, unresolved work and retention limits. |
| NOTES time spent | Implementation session: approximately 97 minutes; UI follow-up: approximately 12 minutes. Personal planning/review time and this final review are not yet consolidated into a total. User input requested. |
| Personal exercise feedback | Implementation observations are recorded. User's own feedback has been requested and is not fabricated. |
| AI conversation disclosure | Prompts, planning responses and partial activity records are tracked in ai/. NOTES explicitly discloses omitted progress/tool output and the absence of a complete platform export. README permits disclosed omissions; a fuller export/share link is optional. User preference requested. |
| Committed NOTES and submission files | NOTES and existing ai/ records are tracked and committed. This review and documentation corrections are committed with the finishing pass. |
| Own GitHub repository and send its link | Pending user submission. No Git remote is configured at review time. No publication was requested or performed. |

## Checks repeated during this review

These passed on the current application source:

```sh
go test ./...
go vet ./...
node --test services/executor/web/app.test.mjs
python3 -m unittest discover -s scripts/tests
git diff --check
```

Ordinary Go tests skip opt-in integration checks without `LAB_TEST_*` variables.
This review used the committed real-service evidence; it did not rerun the
33-scenario Docker campaign or the browser campaign. The original instrument,
public models and acceptance script have no diff from `995c6b6`. Required Go
parameter/result types were compared with the original import and match; local
parameter names may differ without changing the public signature.

The fault campaign predates the web UI. The UI's later Chromium checks,
independent ledger/data-hash checks, and seven-check HTTP acceptance passed
separately, as documented in web-ui-validation.md. Raw artifacts are ignored by
Git; selected portable evidence is committed under validation-evidence/.

## Inputs before submission

1. Supply a rough total including personal planning/reading/review time; keep
   the recorded assisted-session timings clear and avoid double-counting overlap.
2. Supply personal feedback: what was unclear or annoying, and what to change.
3. Confirm the existing disclosed partial AI records are the intended submission,
   or add a complete export/share link.

The first two are the remaining README write-up gaps. The third is a disclosure
choice, since the README allows omissions when they are stated. Publishing the
repository and sending its link remain the user's final submission steps.
