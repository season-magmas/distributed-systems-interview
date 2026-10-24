# Web UI implementation activity record

User request: “Create a simple web ui using the graphql endpoint that a scientist
can use to read the record, start runs and follow them.”

Implemented a local UI in the existing executor, following the Sites skill's
local workflow. No hosted Site was created. Chose embedded buildless HTML/CSS/JS
and two-second GraphQL polling. Kept create/start/cancel as separate mutations.

Added and ran failing route/JavaScript behavior tests before implementation;
committed those, then committed the working implementation. Added real Chromium
checks and a disposable-stack runner with ledger/hash verification and HTTP
acceptance. Screenshots were inspected, including the responsive layout.

An initial temporary wrapper called a nonexistent `verify` method after the
browser checks passed, so it stopped and cleaned up before acceptance. Corrected
the call to the existing `assert_records` helper in the retained runner and reran.
The development executor's original bind mount was rejected by Docker Desktop;
started it with a temporary Compose override using its baked-in config instead.
No developer databases or driver volumes were recreated.

A final browser navigation check covers polling after returning with browser
Back; the page resumes polling on `pageshow` after stopping on `pagehide`.

This is a partial conversation/activity record, not a platform transcript.
Repeated commentary and most raw tool output are omitted. See NOTES.md and
`docs/web-ui-validation.md` for observed checks and limits.
