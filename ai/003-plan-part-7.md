# Part 7: evidence, notes, and submission readiness

Status: submission specification added during the plan review, 2026-10-24.
Implementation and runtime validation remain outstanding. This closes the missing
detailed plan for the README's required note; it does not claim work is complete.

## README requirement coverage

| Requirement | Specification and verification |
| --- | --- |
| Robust executor/driver NATS communication and truthful records | Parts 1–2 define evidence, durable handoffs, and recovery; parts 3–5 test faults |
| One job per physical device, concurrent independent work | Parts 1, 2, and 4 define scheduling and reservations; acceptance and contention scenarios verify |
| Preserve fixed instrument, API, Go signatures, models, and data references | Parts 1, 4, and 6 plus final compatibility checks below |
| GraphQL on the same port; tools start and follow runs | Part 6 provides mutations and polling, sharing the existing HTTP application operations |
| Actual plate-reader data stored correctly | Parts 1–2 and 4 verify bytes/hashes; pre-spool crash loss remains an explicit limitation |
| Happy-path acceptance and additional chaos evidence | Parts 3–5 and the final gate, for both supplied workflows |
| Required note, time, exercise feedback, and AI conversations | This part and the completed NOTES.md |

## Required note

Complete and commit `NOTES.md` before submission. It must stand on its own and
cover all six required topics plus exercise feedback:

1. Actual topology: subjects, streams, consumers, publication/acknowledgement
   boundaries, state ownership, and recovery behavior.
2. Decisions and alternatives: why the chosen design fits this exercise, including
   single-process ownership, outbox, spool, retries, and cancellation semantics.
3. Implemented GraphQL schema and rationale, including create/start/cancel examples
   and polling. Link to the schema source and describe the shared HTTP operations.
4. Fault experiments: exact commands, seeds, settings, observed results, artifacts,
   failures, fixes, and retests. Distinguish process tests from controlled component
   tests and identify boundaries that a random run did not exercise.
5. Known limitations and unproven behavior, with intentional boundaries explained.
6. Approximate actual time spent, separating planning, implementation, testing, and
   documentation where known. Do not infer elapsed effort from commit timestamps.

Record the user's actual feedback on unclear or annoying aspects and suggested
improvements; do not invent their opinion. If time estimates or feedback are missing,
ask for those specific facts at wrap-up rather than manufacturing them.

Use concise summaries in the note with detailed commands/evidence in
`docs/validation.md` when helpful. This is a documentation default, not a required
new logging system. Link retained artifacts that reviewers can actually access.

## Explicit boundaries

Document that the fixed instrument cannot atomically persist a measurement into
the adapter spool. A crash in that window can lose real data permanently. Reporting
that loss truthfully does not satisfy lossless preservation of every read: it is a
known limitation against the README's data objective. Never claim all failures are
solved merely because the API avoids false success.

Also state the assumptions of intact durable volumes and one owner per device,
the absence of replicated failover, unresolved runs during unavailable evidence,
unpaginated listing/retention limits, and physical timestamp limitations across
retries. Preserve evidence rather than hiding these behind general reliability claims.

## Final validation gate

- Check all public signatures, HTTP paths/JSON/status values, and `s3://` references
  against the README. Confirm `instrument.go` is unchanged from the supplied code.
- Run applicable unit/integration/coverage/race checks and the completed routine
  and extended suites, with GraphQL enabled. Extended includes routine coverage,
  so one full extended pass can satisfy both without redundant reruns.
- Run unchanged acceptance checks for both provided workflows with all instrument
  fault percentages zero. Record the actual seven-check results for each.
- Preserve fail-fast semantics: collect diagnostics and clean owned resources on
  the first failure, mark subsequent scenarios not run, fix and retest before
  claiming the final gate. HTTP-only results from before part 6 are intermediate.
- Tie results to the tested implementation revision and configuration; retest
  affected behavior after changes. Documentation-only edits need consistency and
  whitespace review, not a repeated full fault campaign.
- Remove submission placeholders or label genuinely outstanding facts explicitly.
  An unexecuted required check remains incomplete, never implicitly passed.

## AI record and handoff

Include actual conversation are exports under `ai/` and have been captured in git as we progress.

Commit the note, selected evidence, and conversation records. Verify that the final
repository includes them and that referenced files/links are usable. Report the
implementation commit, test outcomes, and remaining limitations to the user.

