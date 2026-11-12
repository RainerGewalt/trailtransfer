# Changelog

All notable changes to TrailTransfer are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.2] - 2026-11-12

Fixes from an internal review of 0.1.1 (8 November 2026). Upgrade if a
worker receives commands from senders you do not fully control. Three
changes can refuse commands or configurations that 0.1.1 accepted; they are
listed under "Changed" with what to do.

### Security

- **Targets outside the allowed path.** The policy compared targets as
  plain text, so a target that spelt a path outside the allowlisted one could
  pass and rclone wrote outside it. Sources and targets are now parsed and
  normalised before the comparison; parent segments in any spelling, encoded
  separators, backslashes, control characters, rclone on-the-fly backends
  and connection-string parameters are refused, for targets as for sources.
  For local paths, symbolic links are resolved and the resolved path must
  stay inside the resolved allowed root as well, which also closes a source
  that is a link out of its root. rclone receives, and the record names, the
  normalised form that was checked.
- **A command could take the worker off the broker.** A `job_id` or
  `target_job_id` that is not a valid MQTT topic level led the worker to
  publish to an invalid topic; the broker disconnected it and the worker did
  not recover until it was killed. Both are now limited to 1 to 128
  characters from `A-Z a-z 0-9 . _ -`; a command with another `job_id` is
  logged and dropped. The MQTT message callback now only queues commands and
  never waits for a publish, and commands above 64 KiB are rejected.
- **`move` ignored `allow_delete: false`.** `move` deletes the source after
  the transfer; it is now refused unless the policy sets `allow_delete: true`.
- **A dry run was recorded as a transfer.** A job that ran as a dry run
  (for example through `dry_run_default`) was sealed with the counters of
  the transfer rclone would have made. Records now carry `dry_run`; a dry run
  records `files_transferred` and `bytes_transferred` as 0, and its status
  says so.

### Fixed

- **Journal write failures.** A result that could not be written to the
  journal was published unsealed, without a log line, and the worker went on
  as `healthy`. Now nothing is published unsealed; the error is logged, the
  sealer refuses every later record, health reports `unhealthy` with the
  reason, and new jobs are refused until the worker is restarted.
- **Size limit.** Files above `max_file_size_mb` are skipped by rclone
  without an error. The record of a job that ran under the limit now carries
  `max_file_size_mb`, and its status says that larger files were excluded and
  not counted.
- **Status queries.** A `status` command reported `completed` for any
  finished job, including failed ones, and published on the queried job's
  topic. It now answers on its own topic with `target_job_id` and
  `target_status`, the stored outcome.
- **Typos in the policy and the config file** were ignored, so a misspelt
  `dry_run_default` turned every command without `dry_run` into a real
  transfer. Both files are now read strictly; see "Changed".
- **`verify` says what it did not check**: signatures without a key or on
  unsigned records, records cut off after the last record, and the
  transferred files. A journal whose first record continues a segment that is
  not in the file is noted as "previous segment not in this journal"; a
  journal without records is reported as `EMPTY` with exit code 1 instead of
  `OK`.

### Changed

- **`job_id` character set.** Commands whose `job_id` uses characters other
  than `A-Z a-z 0-9 . _ -` or is longer than 128 characters are dropped.
  Senders that use other characters need to change their ids.
- **Strict policy and config.** An unknown or misspelt key, an action other
  than `copy`, `move`, `sync` and `check` (including `cancel` and `status`,
  which are always available), an allowlist entry with a parent segment or a
  relative local path, or a negative `max_file_size_mb` now make
  `validate-config` fail and stop the worker from starting. Run
  `trailtransfer validate-config` before upgrading.
- **`move` needs `allow_delete: true`.** Policies that allow `move` and rely
  on it must set `allow_delete: true`.
- Source names that only contain two dots, such as `report..2026`, are no
  longer refused.
- Rejection details for targets now read "is not under any allowed_targets
  entry".
- A rejection now publishes its sealed result before its `rejected` status
  event, in the same order as jobs that ran.

### Added

- `make negative-paths` (`scripts/negative-paths.sh`): the edge cases above
  on a local Mosquitto and rclone, without Docker (`docs/TESTING.md`, L4).
- Payload fields `dry_run` and `max_file_size_mb`, status fields
  `target_job_id` and `target_status`, and a `detail` in health events.
  All are additive.
- An evaluation report issue form, a support statement (best effort, no paid
  support) and the private vulnerability-reporting path in `SECURITY.md`
  (#9).
- `make first-use` installs the pinned trail-evidence verifier for its L3
  step instead of skipping it when none is on `PATH` (#11).

### Removed

- `docs/COMPLIANCE.md`, and the compliance and service statements in
  `docs/CONCEPT.md` and `CONTRIBUTING.md`. What the operator has to provide
  for the journal is now in `docs/EVIDENCE_MODEL.md`; the security model, the
  test behind each threat and the known limits (no TLS to the broker,
  deduplication only within one process, pattern-based redaction) are in
  `SECURITY.md`.

## [0.1.1] - 2026-11-07

### Security

- **`rclone_exit_code` only when rclone ran.** If rclone could not start (for
  example, not installed), the failed record used to carry exit code 0. It
  now has no exit code; the start error is in `errors`.
- **`verify` with flags after the path.** `verify journal.jsonl --pubkey
  k.pub` ignored the key and reported OK without checking signatures. Flags
  are now accepted on both sides of the path; a stray argument is a usage
  error (exit 2).
- **`verify --pubkey` passed unsigned journals.** With a key, a journal
  without any signature was reported as OK with "0 signature(s) verified",
  so someone who could rewrite the journal passed the check with the
  operator's key. With `--pubkey` every record must now carry a valid
  signature: a missing one fails with `signature_invalid` ("record is not
  signed"), the same code and message as the trail-evidence verifiers with
  `--require-signatures`, and the summary counts the unsigned records. A
  journal in which signing was switched on later now fails with the key;
  start a new journal when you enable signing. Without `--pubkey` nothing
  changes.

### Fixed

- **Version of `go install` binaries.** They reported `dev` and wrote it into
  every record; they now take the module version from the build info.
  Versions are reported without a leading `v`, also in the container image,
  whose build passed the tag name (`v0.1.0`).
- **Demo input folder.** `upload_folder/` is in the repository, so Docker no
  longer creates it as root and you can put files into it.
- **Docker demo.** `docker compose up --build` failed for everyone: the
  `minio/minio` and `minio/mc` images can no longer be pulled from Docker Hub
  or quay.io. The demo S3 target is now rclone's own S3 server
  (`rclone serve s3`) from the same pinned rclone 1.68 image the worker
  uses; the remote is renamed from `minio-demo` to `s3-demo` in the example
  rclone config, policy, commands and docs.
- **Docker demo on Linux.** The worker runs as `nonroot`, but Docker created
  `./evidence` as root, so the worker could not open its journal and
  restarted in a loop. A demo-only one-shot step now creates the journal file
  writable for everyone before the worker starts, which also lets
  `make verify-demo` read it from the host.
- **Quickstart.** Its first command, `examples/commands/check.json`, failed
  on every fresh stack (`file not in S3 bucket trailtransfer`): the demo
  bucket did not exist yet, and the check compared every file in
  `upload_folder/`. The Quickstart now copies your images first and then
  checks them; `check.json` uses the same filters as `copy.json`.

### Added

- **`make first-use`** (`scripts/first-use.sh`): walks through the README and
  the Quickstart from a fresh clone, or with `RELEASE=x.y.z` against the
  published tag, `go install` and image, and stops at the first step that
  does not behave as documented. Testing levels in `docs/TESTING.md`.
- **Command-line tests for `verify`**: a wrong key before or after the path
  fails with `signature_invalid`, the right key passes on either side, a
  stray argument is a usage error, an edited record fails with
  `hash_mismatch`, and without a key the output says that signatures were
  not checked. With a key, unsigned, partially signed and re-sealed journals
  without signatures fail, and a missing signature reads differently from
  one that does not verify.

### Changed

- Documentation corrected after a first-use check from a fresh clone: an
  Install section in the README, the `verify [--pubkey] <journal>` order,
  the roadmap with the 0.2.0 milestone, the single-branch release flow in
  `docs/RELEASE.md`, the signing-key ownership for the nonroot image in
  `.env.example`, the published trail-evidence, and several stale
  statements in `docs/`.
- Wording on evidence, compliance and TrailMQ made exact: no "audit-ready"
  or "non-repudiation"; the README and `docs/EVIDENCE_MODEL.md` say what the
  chain detects and what it does not (records cut off at the end of the
  journal); `docs/COMPLIANCE.md` no longer maps the
  worker's machine signatures to Part 11 electronic signatures; the TrailMQ
  section describes only what TrailMQ does today (role and topic decisions
  for the worker's MQTT traffic) and says that it does not read the journal.

## [0.1.0] - 2026-11-02

First release of TrailTransfer, the Go successor of FileFlux.

### Added

- **Product core** — MQTT command → strict schema validation → local policy
  decision → controlled rclone execution (argv, never a shell) →
  status/progress/result events → sealed evidence record.
- **CLI** — `run`, `validate-config`, `print-capabilities`, `verify`, `keygen`,
  `version`.
- **Policy engine** — allowlisted actions, source/target roots,
  `max_parallel_jobs`, `max_file_size_mb`, `dry_run_default`, `allow_sync_delete`,
  `allow_broadcast_commands`; `policy_hash` stamped into every result.
- **Evidence model** — results and rejections are sealed as **Trail Evidence
  Record v1** (`trail.evidence/1`), the evidence format shared across the
  Trail projects (reference specification in the upcoming sister project
  trail-evidence): RFC 8785 (JCS) canonical hashing via
  `github.com/gowebpki/jcs` (byte-identical with the reference
  implementation; values needing exact decimals travel as strings), `content_hash`,
  `chain_hash` over a fixed record core, `seq`/`prev_hash` chain with
  `chain_restart`/`anchor_hash` on every process start, `producer`, `time`,
  `actor` and `policy` members, append-only JSONL journal, and optional
  Ed25519 signatures over the ASCII `chain_hash` (`keygen`, `verify --pubkey`).
- **`trailtransfer verify`** — checks every record of a journal and reports
  `schema_invalid`, `hash_mismatch`, `chain_break`, `seq_gap`,
  `signature_invalid` and informational `restart` findings separately; one
  damaged record no longer hides the rest.
- **Schemas** — `schemas/trail-evidence-record.schema.json` (strict generic
  record) and `schemas/transfer-job-result.schema.json` (TrailTransfer result
  payload); committed canonicalisation test vectors under
  `internal/evidence/testdata/vectors.json`.
- **Configuration** — `policy_id` / `TRAILTRANSFER_POLICY_ID` names the policy
  in evidence records (default: the policy file name).
- **Docker** — multi-stage Go build, pinned rclone, distroless non-root runtime,
  one-command `docker compose up --build` demo with Mosquitto and MinIO.
- **Project files** — `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, this changelog,
  GitHub issue/PR templates, CODEOWNERS, `docs/GIT_WORKFLOW.md`,
  `docs/RELEASE.md`, and a `Makefile` (`test`, `lint`, `demo`, `verify-demo`).

### Changed

- Documentation describes the current **Go** implementation
  (`docs/ARCHITECTURE.md`, `docs/CONCEPT.md`) instead of the Rust→Go migration
  blueprint.
- CI runs on `main` and on release tags; `main` is the only long-lived branch
  (see `docs/GIT_WORKFLOW.md`).
- The capabilities event reports `evidence_schema` (`trail.evidence/1`) instead
  of `evidence_schema_version`.
- `.gitignore` hardened to keep secrets, IDE metadata and build artifacts out of
  version control.

### Removed

- The pre-release TrailTransfer evidence envelope (`schema_version`,
  `worker_id`, `issuer`, top-level `recorded_at`/`time_source`, signatures in
  standard base64 over digest bytes) and `schemas/result-envelope.schema.json`.
  No release carried that format; `verify` reports such lines as
  `schema_invalid`. Migration notes: `docs/MIGRATION.md`.
- Tracked `.env` (only `.env.example` ships) and `.idea/` editor metadata,
  including the stale `super-fast-smb-image-uploader.iml`.
- Stale `create_sftp_test_cases.sh` demo helper.

### Notes

- The active runtime is Go only. The original Rust sources are retained as a
  non-production migration reference under `legacy/rust/`.
- TrailTransfer records are tamper-evident, not immutable: WORM retention,
  trusted time and key management remain operator responsibilities. No GxP,
  Part 11 or certification claim is made.

[Unreleased]: https://github.com/RainerGewalt/trailtransfer/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/RainerGewalt/trailtransfer/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/RainerGewalt/trailtransfer/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/RainerGewalt/trailtransfer/releases/tag/v0.1.0
