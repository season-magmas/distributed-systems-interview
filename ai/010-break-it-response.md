# Ten-minute break-it exercise

Source tested: `c278e9e` (application code from `30fb0fb`). Approximately ten
minutes were spent, 06:20–06:30 UTC on 2026-10-25. Application code and development workflow configuration
were not modified. Faults targeted disposable Compose projects; their databases,
ledgers, spools and object stores were separate from the development stack.

## What I established

Across seven fully verified campaign phases, **64 runs and 293 physical
executions** were checked: 41 runs completed, 17 deliberately failed, and six
were cancelled. Every physical outcome in these phases matched the configured seed
and fault percentages. I independently recomputed the instrument's SHA-256 roll
for each step attempt; this confirms the failures were induced by the tests.

The verification also checked unique job identities, device serialization,
dependency ordering, retry eligibility/budgets, equality between GraphQL execution
history and instrument ledgers, HTTP/GraphQL agreement, cleared terminal recovery
pauses, and stored measurement bytes against instrument hashes. No false
completion, duplicate physical job, unsafe retry or measurement substitution was
found in these verified phases. This is evidence for the tested schedules, not
proof that the implementation has no bugs.

## Concurrent workflows and injected faults

Normal steps were 300ms and hangs were **700ms**. The active-driver-kill attempt
used 1.2s steps to widen its injection window. The object-integrity test used
1.5s steps and a 500ms hang setting. Zero-duration testing uses 0s steps and a 1ms
hang setting, with fault percentages zero.

Four new workflow shapes were exercised alongside PCR Amplification and Triple
Assay: a three-read chain, a three-incubation chain, a two-dispense chain, and
**Wide Join** with six independent roots contending for all three devices, then
a liquid-handler join and final read. A separate **Boundary Reader** workflow
reads before incubation, exposing whether unresolved data incorrectly releases
a dependent step. Fixture definitions are retained with the evidence; they were
copied only into disposable executor containers.

| Phase | Seed | Faults and concurrency | Observed result |
| --- | --- | --- | --- |
| Healthy contention | 0 | 12 concurrent runs across six shapes; no faults | 12 completed, 56 physical executions |
| Persistent fault exhaustion | 101 | Three reader chains with reader FAIL=100%; three incubator chains with incubator HANG=100%; two liquid chains with LH FAIL=100% | Eight failed; reader/incubator stopped after three attempts, LH after one. Ledger: 11 failures, nine hangs |
| Overlapping outages | 2026 | Eight runs; incubator FAIL/HANG=20/30%, reader=20/25%; executor disconnected 5s, storage stopped 6s, incubator disconnected 4s concurrently | Eight completed despite 10 physical failures and 12 hangs; 60 executions total |
| Cancel plus executor restart | 314 | 12 runs; cancel alternating runs; incubator HANG=40%, reader FAIL=30%; kill executor | Six cancelled and six completed; all 30 physical executions accounted for |
| Seeded random chaos, replay | 47 | Nine runs; LH FAIL/HANG=10/10%, incubator=25/25%, reader=20/25%; `chaos.sh random --seed 47 --for 40` | Nine expected failures; 45 executions: 21 completed, nine failed, 15 hung. Every outcome independently matched the instrument roll |
| Zero-duration boundary | 0 | Nine runs of reader chains, incubator chains and Wide Join; 0s work, all faults zero | Nine completed, 42 executions |
| Zero-fault drain after chaos | 0 | Six concurrent PCR/Triple/Wide Join runs; all faults zero | Six completed, 40 executions |

Fault commands used the supplied script, with `COMPOSE_FILE` and
`COMPOSE_PROJECT_NAME` pointing at the relevant disposable project:

```sh
./scripts/chaos.sh disconnect executor 5
./scripts/chaos.sh storage-down 6
./scripts/chaos.sh disconnect incubator-1 4
./scripts/chaos.sh kill executor
./scripts/chaos.sh kill liquid-handler-1
./scripts/chaos.sh kill incubator-1
./scripts/chaos.sh kill plate-reader-1
```

The first three commands ran concurrently. All three driver kills were attempted
concurrently in a later phase; that phase was interrupted by the disk failure
below and is **not counted as a passing recovery test**.

## Integrity boundary: corrupted measurement object

I inserted incorrect bytes at a running read's eventual S3 key. The driver
reported `measurement hash conflict`; the logical run remained running and the
incubation dependency stayed pending. I restarted the reader, then replaced the
object with the exact original bytes recovered from its durable spool. The run
completed and ledger/hash verification passed, without a replacement read.

The important edge is that the system can preserve uncertainty across this fault,
but an operator still needs an external repair procedure. This test repaired a
known corruption using preserved bytes; it does not establish recovery when the
original bytes are gone. Recovery after the reader restart took roughly a
JetStream acknowledgement interval; a short physical hang setting does not
remove transport redelivery latency.

## Infrastructure boundary encountered, not an application pass

A second test filled a reader spool to its 64MiB logical limit. The driver
refused the command with `spool capacity exhausted`, and the ledger showed no
physical start. After removing the test ballast, the intended recovery check
timed out after 55 seconds. It would be incorrect to call this a successful
capacity-recovery test.

The evidence explains the confounder: unrelated drivers in the other test stack
also reported `no space left on device`; Postgres could not extend its files,
then panicked while writing WAL and exited. Docker's shared disk was at **99%**
(39.1GiB filesystem, about 360MiB reported available), with about 36.49GB of
images. Separate Compose projects isolate application resources, but share that
host disk. The tests therefore interfered at the infrastructure layer.

I stopped the affected campaign, retained its logs/snapshots, removed only its
owned resources, and freed disposable Go build cache. Reported available space
rose to about 1.2GiB. I did not prune unrelated images or delete development
volumes. The unfinished run was reported as `running`, with no physical start
and `recovery unresolved: deadline exceeded`; the system did not invent success.
That observation does not prove all records remained correct through ENOSPC.

Operational edge: the executor container was still marked healthy while its
Postgres container had exited. The current health handler is a liveness check,
not a database/messaging readiness check. A disk-space preflight and readiness
signal would make these incidents much easier to distinguish from workflow
faults. No production fix was made during this time-boxed exercise.

The unsupported-device input probe, originally scheduled after capacity recovery,
was not reached and is not counted as tested.

## Final acceptance and cleanup

After the replay, all instrument fault percentages were restored to zero and
`./scripts/acceptance.sh` passed all seven checks (seven physical executions,
about four seconds). The separate integrity-repair run and final acceptance add
two completed runs and nine executions to the 64-run campaign totals above.
All 17 terminal failed campaign runs have seed-backed instrument fault causes;
the unresolved ENOSPC attempts are explicitly excluded from that count.

All test-owned containers, volumes and networks were removed. The first campaign
needed manual cleanup after diagnostics were interrupted while its database
was down; the boundary and replay runners completed their cleanup. Test-owned
image tags were removed, and no `lab.test-owner` containers remained. Existing
development containers and volumes were retained; their shared Docker host was
subject to the disk pressure described above.

## Evidence and reproduction

Evidence is retained in [`docs/validation-evidence/break-it/`](../docs/validation-evidence/break-it/):
`causation-checks.json` records independent seed verification; the campaign,
boundary results, corruption snapshot, disk-error excerpt and scratch runners
preserve passing and unsuccessful attempts. Full command logs and snapshots are
in ignored artifacts:

- `lab-test-20261025t062135z-12254`: first four verified phases and interrupted driver kills.
- `lab-test-20261025t062303z-13091`: object repair and capacity test confounded by ENOSPC.
- `lab-test-20261025t062650z-14404`: remaining cases replayed after freeing space.

The archived `campaign.py` and `boundaries.py` are executable scratch test drivers
using the existing `scripts/system_test.py` helpers. Run them from this checkout
with Python and Docker; they create and delete test-owned resources. `replay.py`
records the exact continuation, which reused the initial campaign's image tags;
those tags must exist or its image assignments must be replaced with a build.
The report is a partial AI activity record; repeated progress and raw tool
output are summarized rather than exported verbatim.
