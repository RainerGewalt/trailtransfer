# Testing

How TrailTransfer is tested: which levels exist, what each one proves, and
how to run it. The `Makefile` is the entry point; this page explains what its
targets mean. The levels follow the testing concept of the Trail projects.

## Levels

**L1 Unit.** Pure logic decided from one function: command decoding and
schema rules (`internal/commands`), policy validation (`internal/policy`),
the action-to-argv mapping and filter patterns (`internal/rclone`), JCS
canonicalisation and hashing (`internal/evidence`). Go `*_test.go` beside the
code. Runs with `make test`; needs nothing.

**L2 Component.** A unit with its real collaborators, in process: the sealer
and verifier on temporary journal files (`internal/evidence`), the job
manager with a fake rclone runner and a fake MQTT sink (`internal/jobs`), and the command line itself
(`cmd/trailtransfer/verify_cli_test.go`), which re-executes the test binary as
`trailtransfer verify` so that flag parsing and exit codes are covered, not
only the library underneath. Runs with `make test`; needs nothing.

**L3 Conformance.** TrailTransfer writes Trail Evidence Records. Its
canonicalisation vectors live in `internal/evidence/testdata/vectors.json`
and are checked by `internal/evidence/vectors_test.go`. The journals of a
real run are also checked by the independent
[trail-evidence](https://github.com/RainerGewalt/trail-evidence) verifier
inside `make first-use` when `trail-evidence` is on `PATH`: the demo journal
and the signed journal must be `VALID`, an edited copy `INVALID`. Without the
verifier that step is reported as skipped, so a release check runs with it
installed.

**L4 Scenario.** Product behaviour on the real stack: worker, Mosquitto and
the S3 target from `docker-compose.yaml`, commands sent with
`mosquitto_pub`, results read from the worker's topics and journal. These
scenarios run inside `make first-use`: a copy with a filter, a check, two
policy refusals, journal verification inside the container and on the host,
an edited journal, the unsigned demo journal checked with a key (must fail),
and a worker with a signing key verified with the right and with a wrong
public key.

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
9000. It runs under its own Compose project name, so a demo that is already
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

## Commands

- `make test`: all Go tests with the race detector (L1, L2, vectors of L3).
- `make lint`: gofmt check and `go vet`.
- `make first-use`: L3 to L5 against the committed `HEAD` from a fresh clone.
- `make first-use RELEASE=x.y.z`: L3 to L5 against the published release.

## CI

`.github/workflows/ci.yml` runs gofmt, `go vet`, `go test -race`, the build
and a check of the Compose file on every push and pull request. `make
first-use` is not part of CI: it needs Docker networking on fixed ports and,
for a release, the published artifacts. It is run by hand before and after
every tag.

## Data and secrets

Synthetic values only. The first-use check generates its own keys in its
working directory and deletes them afterwards; the demo S3 credentials in
`docker-compose.yaml` are demo values for a local container.
