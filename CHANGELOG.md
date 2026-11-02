# Changelog

All notable changes to TrailTransfer are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/).

Nothing has been tagged yet; everything below is **Unreleased** and building
toward the first `v0.x` release.

## [Unreleased]

### Added

- **Product core** — MQTT command → strict schema validation → local policy
  decision → controlled rclone execution (argv, never a shell) →
  status/progress/result events → sealed evidence record.
- **CLI** — `run`, `validate-config`, `print-capabilities`, `verify`, `keygen`,
  `version`.
- **Policy engine** — allowlisted actions, source/target roots,
  `max_parallel_jobs`, `max_file_size_mb`, `dry_run_default`, `allow_sync_delete`,
  `allow_broadcast_commands`; `policy_hash` stamped into every result.
- **Evidence model** — RFC 8785 (JCS) canonical hashing, `content_hash`,
  `chain_hash`, `seq`/`prev_hash`, append-only JSONL journal, independent
  `trailtransfer verify`, and optional Ed25519 signatures (`keygen`,
  `verify --pubkey`). Rejections are sealed evidence records too.
- **Docker** — multi-stage Go build, pinned rclone, distroless non-root runtime,
  one-command `docker compose up --build` demo with Mosquitto and MinIO.
- **Project files** — `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, this changelog,
  GitHub issue/PR templates, `docs/RELEASE.md`, and a `Makefile`
  (`test`, `lint`, `demo`, and `verify-demo`).

### Changed

- Documentation now describes the current **Go** implementation
  (`docs/ARCHITECTURE.md`, `docs/CONCEPT.md`) instead of the Rust→Go migration
  blueprint.
- `.gitignore` hardened to keep secrets, IDE metadata and build artifacts out of
  version control.

### Removed

- Tracked `.env` (only `.env.example` ships) and `.idea/` editor metadata,
  including the stale `super-fast-smb-image-uploader.iml`.
- Stale `create_sftp_test_cases.sh` demo helper.

### Notes

- The active runtime is Go only. The original Rust sources are retained as a
  non-production migration reference under `legacy/rust/`.

[Unreleased]: https://github.com/RainerGewalt/FileFlux/compare/master...HEAD
