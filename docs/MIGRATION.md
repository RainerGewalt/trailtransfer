# Migration: FileFlux → TrailTransfer (Rust → Go)

## Summary

FileFlux was a Rust "super-fast-smb-image-uploader": an MQTT-driven worker that
implemented SMB/SFTP uploads itself, with a `start`/`stop` command model and
random per-run job IDs. It has been transformed into **TrailTransfer**, a
**Go** worker that orchestrates **rclone** under a local policy and emits
audit-ready evidence events.

The active runtime is now **Go only**. The Rust sources are retained as a
migration reference under [`legacy/rust/`](../legacy/rust/) and are **not** a
build path (no CI, no Docker stage builds them).

## What changed

| Area | FileFlux (Rust) | TrailTransfer (Go) |
|---|---|---|
| Language / runtime | Rust | Go (`cmd/trailtransfer`, `internal/*`) |
| Transfer | own SMB/SFTP code | rclone as a subprocess (argv, no shell) |
| Command model | `start` / `stop` + `options` | `copy/move/sync/check/cancel/status`, closed schema |
| Job ID | random UUID per run | client-supplied, required, **deduped** |
| Policy | none | `worker-policy.yaml` — the security boundary |
| Concurrency | file-level only | `max_parallel_jobs` at job level |
| rclone.conf | generated from SMB/SFTP env | operator-provided, read-only, never logged |
| Result event | camelCase, ad-hoc | sealed JCS envelope: `seq`/`prev_hash` chain, `content_hash`, `command_hash`, `policy_hash` |
| Topics prefix | `image_uploader` (configurable) | `trailtransfer` |
| Env prefix | mixed (`MQTT_*`, `SMB_*`, `SFTP_*`) | `TRAILTRANSFER_*` |

## Environment variables

Old FileFlux vars (`MQTT_ROOT_TOPIC`, `SMB_*`, `SFTP_*`, `WORKER_ID`, …) are
**removed**, not aliased — the SMB/SFTP-specific ones have no meaning now that
rclone owns transport. Use `TRAILTRANSFER_*` (see `.env.example`). Notable
rename during this task: `TRAILTRANSFER_RCLONE_PATH` → `TRAILTRANSFER_RCLONE_BINARY`.

## Naming

`FileFlux/fileflux/FILEFLUX` → `TrailTransfer/trailtransfer/TRAILTRANSFER` across
code, README, docs, Docker, compose, CI, examples and MQTT topics. The GitHub
repo is still named `FileFlux`; the Go module is `github.com/RainerGewalt/trailtransfer`,
so renaming the repo to `trailtransfer` is the one remaining external step.

## Removed / archived

- **Archived** to `legacy/rust/`: `Cargo.toml`, `Cargo.lock`, `src/*.rs`.
- **Removed** CI: `rust.yml`, `docker-image.yml` (Rust build/test) and
  `docker-publish.yml` (superseded by `docker.yml`). New: `ci.yml`, `docker.yml`.
- Rust `Dockerfile` / `docker-compose.yaml` replaced with Go equivalents.

## Open points

- Rename the GitHub repository `FileFlux` → `trailtransfer` (the Go module is
  already `github.com/RainerGewalt/trailtransfer`).
- Harden transfer authorization with policy-owned routes, explicit destructive
  action guards, and component-aware local and remote containment.
- Persist job-ID deduplication and terminal-result delivery across worker and
  broker restarts.
- Add stronger evidence levels, file manifests, trusted timestamps, and
  immutable external retention as later, separately reviewed work.

## Done since the initial migration

- `docs/CONCEPT.md` and `docs/ARCHITECTURE.md` de-Rusted; ARCHITECTURE now
  describes the current architecture instead of a migration blueprint.
- Untracked `.env` (only `.env.example` ships) and `.idea/` editor metadata,
  including `super-fast-smb-image-uploader.iml`; removed both copies of the
  stale `create_sftp_test_cases.sh` helper.
- Added `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `CHANGELOG.md`, GitHub
  issue/PR templates, `docs/RELEASE.md`, and a `Makefile`.
