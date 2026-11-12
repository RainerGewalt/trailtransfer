# Testing

How TrailTransfer is tested: which levels exist, what each one proves, and
how to run it. The `Makefile` is the entry point; this page explains what its
targets mean. The levels follow the testing concept of the Trail projects.

## Levels

**L1 Unit.** Pure logic decided from one function: command decoding, schema
rules and the `job_id` character set (`internal/commands`), policy
validation, path normalisation and strict policy loading
(`internal/policy`), strict config loading (`internal/config`), the
action-to-argv mapping and filter patterns (`internal/rclone`), the
redaction filter (`internal/logging`), JCS canonicalisation and hashing
(`internal/evidence`). Go `*_test.go` beside the code. Runs with `make test`;
needs nothing.

**L2 Component.** A unit with its real collaborators, in process: the sealer
and verifier on temporary journal files (`internal/evidence`), the policy
against real symbolic links in a temporary directory (`internal/policy`), the
job manager with a fake rclone runner and a fake MQTT sink, including one
that never completes a publish (`internal/jobs`), the manager, sealer and
health loop on a real journal whose writes fail under a file-size limit
(`internal/jobs/journal_failure_linux_test.go`, Linux only), and the command
line itself (`cmd/trailtransfer/*_cli_test.go`), which re-executes the test
binary as `trailtransfer verify` or `validate-config` so that flag parsing,
output and exit codes are covered, not only the library underneath. Runs
with `make test`; needs nothing.

**L3 Conformance.** TrailTransfer writes Trail Evidence Records. Its
canonicalisation vectors live in `internal/evidence/testdata/vectors.json`
and are checked by `internal/evidence/vectors_test.go`. The journals of a
real run are also checked by the independent
[trail-evidence](https://github.com/RainerGewalt/trail-evidence) verifier
inside `make first-use` when `trail-evidence` is on `PATH`: the demo journal
and the signed journal must be `VALID`, an edited copy `INVALID`. Without the
verifier that step is reported as skipped, so a release check runs with it
installed.

**L4 Scenario.** Product behaviour on a real broker and a real rclone, with
commands sent with `mosquitto_pub` and results read from the worker's topics
and journal. Two sets:

- *The documented path*, inside `make first-use` on the Docker demo stack:
  a copy with a filter, a check, two policy refusals, journal verification
  inside the container and on the host, an edited journal, the unsigned demo
  journal checked with a key (must fail), and a worker with a signing key
  verified with the right and with a wrong public key.
- *The edges*, `make negative-paths` (`scripts/negative-paths.sh`), without
  Docker: the worker binary, a local Mosquitto and rclone, with an rclone
  alias remote on a temporary folder as the target. Each check starts its own
  worker with its own `worker_id` and journal: a target with `..` (refused,
  nothing written outside the allowed path) and a target spelt with `//` and
  `./` (allowed, lands in the same place); a source that is a symbolic link
  out of the allowed root (refused, nothing copied); a dry run (record says
  `dry_run: true` and zero transferred files and bytes, nothing at the
  target); a file over `max_file_size_mb` (record names the limit); `move`
  with `allow_delete: false` (refused, the source file stays); `job_id`s with
  `#`, `+` and `/` followed by a valid command (the valid one is answered, the
  broker log shows no disconnect, nothing ran, the worker stops within 5 s of
  SIGTERM); a status query after a failed job (answer on its own topic says
  `failed`, nothing is added to the failed job's topic, an unknown job's
  topic stays empty); a journal that cannot be written, by running the worker
  under `ulimit -f` below the journal's size (no unsealed result, an ERROR
  log line, health `unhealthy`, the next job refused, the journal still
  verifies); and a policy with a misspelt key or an unknown action
  (`validate-config` exits 1). It needs `mosquitto`, `mosquitto_pub`,
  `mosquitto_sub`, `rclone` and a free port (`NEG_PORT`, default 15931). It
  reports every check and exits 1 if any failed; `TT=/path/to/binary` runs it
  against another build, which is how it was shown to fail before the fixes.

**L5 First use.** `make first-use` (`scripts/first-use.sh`) walks through the
README and `docs/QUICKSTART.md` as a new user would and stops at the first
step that does not behave as documented:

- `make first-use` checks the committed `HEAD` from a fresh clone, so files
  that exist only in a working copy cannot help. This is the check before a
  tag.
- `make first-use RELEASE=0.1.1` checks what was published: the tag `v0.1.1`
  from GitHub, `go install …@v0.1.1` (which must report `0.1.1`) and the GHCR
  image `0.1.1` (which must report `0.1.1` and run the demo). This is the
  check after a tag.

It needs what the README asks of a user: git, Go 1.26 or newer, Docker with
Compose v2, `mosquitto_pub` and `mosquitto_sub`, and free ports 1883 and
9000 (`MQTT_PORT` and `S3_PORT` move the demo stack's host ports, for
example on a machine that already runs a broker). It runs under its own Compose project name, so a demo that is already
running is not touched, and removes its containers, volumes and working
directory afterwards (`FIRST_USE_KEEP=1` keeps the directory).

No tag without a passing `make first-use` on the release commit; if the
check after the tag fails, a patch release follows. The result of both checks
is recorded with the release.

## Determinism

A test proves a property with a signal that is ordered with it, never by
waiting. In `make first-use` every wait ends on the message it waits for: the
retained health message shows that the subscription is active and the worker
is online, a job's result event shows that the job is done, and the log line
"subscribed to command topic" shows that a locally started worker can receive
commands. The time bound (`FIRST_USE_WAIT`, 120 seconds) only stops a hang and
is never the assertion.

## Every fix ships with a negative control

A test for a fix has to be shown to fail without the fix: remove or mutate
the fixed code, run the test, record that it fails, restore the file and
check that it is identical. A test that has never failed has not been tested.

Recorded controls:

- Fixes of the internal review of 8 November 2026 (0.1.2). Each mutation
  below was applied on its own, the named tests failed, and the file was
  restored and checked byte for byte with SHA-256:
  - `ValidJobID` accepting any non-empty id:
    `TestDecodeRejectsJobIDsThatAreNotASafeTopicLevel`,
    `TestDecodeRejectsTargetJobIDsThatAreNotASafeTopicLevel`,
    `TestPeekJobIDIgnoresJobIDsThatAreNotASafeTopicLevel`,
    `TestInvalidJobIDsArePublishedNowhere` fail.
  - `Manager.Handle` handling the command synchronously again:
    `TestHandleNeverWaitsForAPublish` fails.
  - `checkSegment` disabled: `TestEvaluateRefusesTargetsOutsideTheAllowedPath`
    and `TestEvaluateRefusesSourcesOutsideTheAllowedRoot` fail.
  - symbolic links not resolved in `contained`:
    `TestEvaluateResolvesSymlinksInLocalSources` and
    `TestEvaluateResolvesSymlinksInLocalTargets` fail.
  - `Evaluate` handing back the command's spelling instead of the
    normalised form: `TestEvaluateHandsOnTheNormalisedForm` fails; the
    manager running the command as received instead of the normalised form:
    `TestRunUsesTheNormalisedSourceAndTarget` fails.
  - `move` not gated by `allow_delete`:
    `TestEvaluateRefusesMoveWhileAllowDeleteIsFalse` fails.
  - the policy read without `KnownFields`, or `allowed_actions` not checked:
    `TestLoadRejectsUnknownKeys` or `TestLoadRejectsUnknownActions` in
    `internal/policy`, and `TestValidateConfigFailsOnAPolicyTypo`, fail; the
    config read without `KnownFields`: `TestLoadRejectsUnknownKeys` in
    `internal/config` fails.
  - dry-run counters not zeroed, or `dry_run` not recorded:
    `TestDryRunIsRecordedAsADryRun` (and `TestRealRunIsRecordedAsNoDryRun`)
    fail; the size limit not recorded: `TestSizeLimitIsNamedInTheRecord`
    fails.
  - the sealer resuming after a failed write:
    `TestSealerSealsNothingAfterAJournalWriteFailure` fails; the publisher
    falling back to an unsealed result, health staying `healthy`, or new jobs
    admitted after the failure:
    `TestJournalWriteFailureStopsSealingAndRefusesNewJobs` fails.
  - the status answer published on the queried job's topic:
    `TestStatusQueryReportsTheStoredOutcomeOnItsOwnTopic` fails.
  - `verify` without the "not checked" lines, with an empty journal as OK, or
    without the missing-segment note: `TestVerifyNamesWhatItDidNotCheck`,
    `TestVerifyWithTheKeyListsSignaturesAsChecked`,
    `TestVerifyDoesNotCallAnEmptyJournalOK`,
    `TestVerifySaysThatThePreviousSegmentIsNotInTheJournal` fail.
  - The whole patch removed: `make negative-paths` against a binary built
    from the code before the fixes reports 17 of 20 checks failed; with the
    fixes all 20 pass.

- `verify` with flags after the path (fixed after 0.1.0): with the re-parse
  in `cmdVerify` removed, `TestVerifyChecksSignaturesWhenThePubkeyFollowsThePath`,
  `TestVerifyAcceptsTheSigningKeyOnEitherSideOfThePath` and
  `TestVerifyRejectsAStrayArgument` fail; restored, all pass.
- `verify --pubkey` with unsigned records (fixed after 0.1.0): with the
  unsigned-record branch in `VerifyReader` removed,
  `TestVerifyWithAKeyFailsAnUnsignedJournal`,
  `TestVerifyWithAKeyFailsAPartiallySignedJournal` and
  `TestVerifyWithAKeyFailsARecordWhoseSignatureWasRemoved` in
  `internal/evidence`, and `TestVerifyWithAKeyFailsAnUnsignedJournal`,
  `TestVerifyWithAKeyFailsAPartiallySignedJournal` and
  `TestVerifyTellsAMissingFromAWrongSignature` in `cmd/trailtransfer` fail;
  restored, all pass.
- Quickstart order (fixed after 0.1.0): with the old `check.json` as the
  first command, `make first-use` fails at that step with
  `file not in S3 bucket trailtransfer`; with the corrected Quickstart it
  passes.
- `make first-use`, step "Independent verifier trail-evidence (L3)": the
  step installs the pinned trail-evidence (`TRAIL_EVIDENCE_VERSION`,
  default v0.2.1) into the run's own `bin/`, checks the reported version
  and verifies the journals with it; a verifier already on PATH is not
  used. Control: `TRAIL_EVIDENCE_VERSION=v0.0.0-missing make first-use`
  fails at that step with `go install trail-evidence@v0.0.0-missing
  failed … cannot be skipped` (exit 1); with the default it passes. Before
  this change the step printed `skipped` and the run ended green.

## Commands

- `make test`: all Go tests with the race detector (L1, L2, vectors of L3).
- `make lint`: gofmt check and `go vet`.
- `make negative-paths`: the edge-case scenarios of L4 without Docker.
- `make first-use`: L3 to L5 against the committed `HEAD` from a fresh clone.
- `make first-use RELEASE=x.y.z`: L3 to L5 against the published release.

## CI

`.github/workflows/ci.yml` runs gofmt, `go vet`, `go test -race`, the build
and a check of the Compose file on every push and pull request. `make
first-use` is not part of CI: it needs Docker networking on fixed ports and,
for a release, the published artifacts. `make negative-paths` is not part of
CI either: it needs Mosquitto and rclone on the runner. Both are run by hand
before every tag, and `make first-use RELEASE=x.y.z` after it.

## Data and secrets

Synthetic values only. The first-use check generates its own keys in its
working directory and deletes them afterwards; the demo S3 credentials in
`docker-compose.yaml` are demo values for a local container.
