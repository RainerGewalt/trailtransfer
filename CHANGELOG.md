# Changelog

All notable changes to TrailTransfer are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

Nothing yet. The section below is prepared for the first tag and becomes
`[0.1.0] - 2026-11-02` when the tag is created; see
[`docs/RELEASE_0.1.0_CHECKLIST.md`](docs/RELEASE_0.1.0_CHECKLIST.md).

## [Unreleased → 0.1.0] - 2026-11-02 (prepared, not yet tagged)

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
  trail-evidence): RFC 8785 (JCS) canonical hashing, `content_hash`,
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
- CI runs on `master` and `staging` and on release tags; a pull-request policy
  workflow enforces the branch direction.
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

[Unreleased]: https://github.com/RainerGewalt/FileFlux/compare/master...HEAD
