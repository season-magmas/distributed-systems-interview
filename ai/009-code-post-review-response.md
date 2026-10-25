# Post-review response

The user requested fixes for both findings in `ai/008-code-review-response.md`
and consistent use of Go constants for commands. During implementation they
clarified: “We don't need to worry about making the sql query/updates to use const
for the query etc”. The SQL extraction was reverted accordingly.

Implemented:

- Empty GraphQL run IDs return `null` without using the list-all sentinel.
- UI detail requests use generations so obsolete success, failure, and loading
  updates cannot override newer reads or mutations.
- Shared Go constants cover device IDs, the executor consumer, physical outcomes,
  and the measurement-loss code. GraphQL reuses existing status constants.
- Regression tests cover empty IDs with zero and multiple real database records,
  delayed responses across polling/start/cancel, stale failures/loading updates,
  uncertain mutation responses and read recovery.

Both original bugs were reproduced with failing tests before the fixes. Go
tests, coverage, vet, 13 Node tests, seven Python tests, real-dependency integration,
Chromium checks, HTTP acceptance and executor-kill recovery passed. Race testing
was attempted but could not compile because cgo/the C compiler were unavailable.
Exact commands, coverage, artifact paths and validation boundaries are recorded
in section 11 of `NOTES.md`; portable evidence is under
`docs/validation-evidence/post-review/`.

The browser build preceded reverting the SQL extraction but used the final UI
fix. Final integration and executor-kill checks ran after the reversion. Test
stacks were disposable and cleaned up; existing development volumes were kept.
The full historical chaos campaign was not rerun.

This is a partial activity record, not a verbatim conversation export; routine
progress updates and raw tool output are omitted. Work took approximately five
minutes in addition to the earlier sessions.
