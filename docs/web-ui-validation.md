# Web UI validation — 2026-10-24

Final disposable project: `lab-test-20261024t184521z-49097`.
Browser checks, independent ledger/data hash verification, and all seven HTTP
acceptance checks passed. Cleanup reported no errors. Portable results are in
[`validation-evidence/web-ui/`](validation-evidence/web-ui/). Full screenshots,
Compose configuration, command logs, and ledger snapshots remain in
`artifacts/lab-test-20261024t184521z-49097/` (ignored by Git).

## Reproduce

With Docker, Compose, Python 3, Node.js 20+, npm and Chromium available:

```sh
node --test services/executor/web/app.test.mjs
go test ./...
go test -cover ./...
go vet ./...
python3 -m unittest discover -s scripts/tests
npm install --prefix /tmp/lab-ui-browser playwright-core@1.64.1 --no-audit --no-fund
LAB_PLAYWRIGHT_MODULE=file:///tmp/lab-ui-browser/node_modules/playwright-core/index.mjs \
CHROMIUM_PATH=/usr/bin/chromium python3 scripts/web_ui_test.py
```

All of these checks ran successfully. Integration tests requiring opt-in
`LAB_TEST_*` variables are skipped by ordinary `go test`; the separate browser
runner exercised the real stack. No additional Go race check was run for static
asset serving and the browser-only state changes. The earlier implementation's
race/fault campaign results are retained separately in `validation.md`.

## Observed behavior

- Scientists created a pending run, then started it with a separate mutation.
  Polling reached completed, with a stored plate-reader S3 reference visible.
- Expanded attempts stayed open across polling updates. A bookmark reload
  restored the selected run. Polling continued after browser Back navigation.
- Aborted GraphQL reads retained the completed record, displayed a stale-data
  warning, and disabled mutations until polling recovered.
- A pending run was cancelled through the UI. An aborted create mutation showed
  the uncertainty message; no second create was sent during subsequent polls.
- A missing bookmarked run showed no record and no action controls.
- The 390px viewport had no horizontal overflow. Desktop and mobile screenshots
  were inspected. No JavaScript page errors occurred.
- Independent instrument ledgers matched UI-created runs, job uniqueness,
  dependency order, physical execution IDs, and stored measurement hashes.
- Existing HTTP acceptance passed all seven checks on another run.

The runner creates its own database volumes and namespaced infrastructure,
sets instrument fault percentages to zero, and stops after the first failure.
It removes its containers, volumes, networks and four application image tags;
base images and build cache are shared and retained. Both earlier UI test
projects were also cleaned up.

An initial temporary wrapper used a nonexistent `verify` method after its browser
checks passed. It stopped before acceptance and cleaned up. The retained runner
uses `assert_records`. The subsequent run at
`artifacts/lab-test-20261024t184303z-47946` passed the browser, ledger/hash and
acceptance checks. A final run repeated these checks after adding browser Back
coverage and restoring polling on `pageshow`.

The development executor now serves the UI at port 5002. Docker Desktop rejected
this workspace's config bind path, so a temporary resolved Compose file at
`/tmp/lab-ui-local-compose.json` omitted only the executor config mount. The image
contains the same config. Health and HTML requests succeeded, and the served
JavaScript SHA-256 matched the local source:
`7d7dd993d12fa382498c5f006557ec2fd07741e08a6592812540a812891cfca6`.
No development database or driver volumes were recreated.
