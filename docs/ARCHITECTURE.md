# TrailTransfer Architecture

This document describes the maintained Go runtime in the current repository.
The historical Rust implementation is archived under `legacy/rust/` and is not
part of the production build.

## System overview

TrailTransfer is a single-purpose edge worker. It receives file-transfer
commands over MQTT, evaluates them against a local policy, invokes rclone as a
subprocess, and publishes lifecycle and result events.

```text
MQTT command
    |
    v
strict JSON decoder
    |
    v
local worker policy
    |
    v
job admission and lifecycle manager
    |
    v
fixed rclone argument vector -> rclone subprocess
    |
    v
status, progress, logs, and sealed result envelope
```

The MQTT payload is untrusted. The operator-provided policy and rclone
configuration are local inputs. TrailTransfer never accepts a shell command or
a free-form rclone argument list from MQTT.

## Maintained components

| Path | Responsibility |
|---|---|
| `cmd/trailtransfer` | CLI commands, startup wiring, configuration loading, and shutdown |
| `internal/config` | Runtime configuration, environment overrides, and MQTT topic construction |
| `internal/commands` | Command types and strict JSON decoding |
| `internal/policy` | Action, source, target, and destructive-sync policy checks |
| `internal/jobs` | Admission, in-process job-ID deduplication, concurrency, cancellation, and lifecycle events |
| `internal/rclone` | Fixed argument construction, subprocess execution, and JSON progress parsing |
| `internal/mqtt` | Paho MQTT client, reconnect behavior, subscriptions, publishing, and Last Will |
| `internal/events` | Canonical status, progress, result, log, health, and capabilities payloads |
| `internal/evidence` | JCS hashing, result envelopes, hash chaining, journaling, signing, and verification |
| `internal/logging` | Structured logging and credential-pattern redaction |
| `internal/health` | Retained worker heartbeat publication |
| `internal/version` | Build-time worker version |

The executable entry point is `cmd/trailtransfer`. No code under `legacy/` is
imported by the Go module or copied into the runtime image.

## Startup and configuration

`trailtransfer run` performs the following work:

1. Load the optional YAML runtime configuration and apply `TRAILTRANSFER_*`
   environment overrides.
2. Load the worker policy from `policy_file` and calculate a SHA-256 hash over
   the exact policy bytes.
3. Create the rclone runner, evidence sealer, MQTT client, event publisher, and
   job manager.
4. Subscribe to the worker-specific command topic after each successful MQTT
   connection.
5. Publish retained capabilities and start the retained health heartbeat.
6. Handle commands until an interrupt or termination signal is received.

Transfer backend credentials belong in the operator-provided `rclone.conf`.
TrailTransfer passes that file to rclone with `--config`; it does not generate,
inspect, or publish its contents.

The worker version is stored in `internal/version.Version`. Container and local
builds can set it with the linker assignment:

```text
-X github.com/RainerGewalt/trailtransfer/internal/version.Version=<version>
```

## MQTT interface

The default topic prefix is `trailtransfer`.

| Topic | Direction | QoS | Retained |
|---|---|---:|---:|
| `trailtransfer/{worker_id}/commands` | inbound | 1 | no |
| `trailtransfer/{worker_id}/jobs/{job_id}/status` | outbound | 1 | no |
| `trailtransfer/{worker_id}/jobs/{job_id}/progress` | outbound | 0 | no |
| `trailtransfer/{worker_id}/jobs/{job_id}/result` | outbound | 1 | no |
| `trailtransfer/{worker_id}/jobs/{job_id}/logs` | outbound | 0 | no |
| `trailtransfer/{worker_id}/health` | outbound | 1 | yes |
| `trailtransfer/{worker_id}/capabilities` | outbound | 1 | yes |

The MQTT client uses one outbound connection, automatic reconnect, a clean
session, and a retained Last Will that marks the worker offline. On reconnect,
the worker subscribes again and republishes capabilities.

Broadcast command subscription is not implemented. The worker subscribes only
to its own command topic.

## Command model

The current transfer command model uses direct `source` and `target` fields:

```json
{
  "job_id": "job-001",
  "action": "copy",
  "source": "/data/input",
  "target": "minio-demo:trailtransfer",
  "recursive": true,
  "filters": ["*.jpg", "*.png"],
  "dry_run": false,
  "operator": "production-line-4",
  "reason": "nightly image export"
}
```

The executable actions are `copy`, `move`, `sync`, and `check`. The control
actions are `cancel` and `status`.

`internal/commands.Decode` uses `encoding/json.Decoder` with
`DisallowUnknownFields`. It requires a non-empty `job_id`, rejects unknown
actions, and requires `target_job_id` for control actions. There is deliberately
no `args`, `flags`, or equivalent pass-through field. The published JSON Schema
under `schemas/` documents the external contract; runtime validation is also
implemented in Go and does not execute the schema document dynamically.

`recursive` and `dry_run` are pointer fields internally so the worker can
distinguish an omitted value from an explicit `false`. An omitted `dry_run`
uses the policy's `dry_run_default`. Filter values become individual rclone
`--include` arguments after validation.

## Current policy boundary

The worker policy is loaded from local YAML. MQTT commands cannot change its
allowlists or limits. The active checks are:

- the action must appear in `allowed_actions`;
- the source must equal or sit beneath one of the `allowed_sources` roots;
- the target must equal or sit beneath one of the `allowed_targets` entries;
- an absolute local target additionally requires `allow_absolute_targets`;
- `sync` additionally requires `allow_sync_delete`, because it can delete data
  from the target;
- admitted jobs are bounded by `max_parallel_jobs`;
- `max_file_size_mb` becomes rclone's `--max-size` argument;
- an omitted command-level `dry_run` uses `dry_run_default`.

`copy` and `check` are non-destructive. `move` deletes source data after a
successful transfer and is currently controlled by its presence in
`allowed_actions`. `sync` may delete target data and has the additional guard
described above. There is no generic delete or purge command.

The current local-source check rejects any input containing `..`, cleans the
path with `filepath.Clean`, and performs a separator-aware root-prefix check.
This prevents ordinary lexical traversal and prefix confusion such as treating
`/data/input-other` as a child of `/data/input`. It does not resolve symbolic
links. Remote targets are compared with configured alias/path strings and are
not read from `rclone.conf` by the policy engine.

Each loaded policy receives:

- `policy_version`, set by the operator; and
- `policy_hash`, calculated from the exact policy file bytes.

Both values are included in terminal transfer results.

## Job admission and lifecycle

`internal/jobs.Manager` owns the in-process execution state:

- `seen` records admitted job IDs and rejects a repeated ID as
  `duplicate_job_id`;
- `running` enforces the policy's `max_parallel_jobs` limit;
- `active` maps running job IDs to `context.CancelFunc` values;
- a mutex protects all three structures.

Deduplication is process-local. Restarting the worker clears `seen`; persistent
job-state semantics are not part of the current implementation.

For an executable command, the manager follows this sequence:

```text
decode failure with a usable job_id -> rejected result
policy denial                       -> rejected result
duplicate or capacity denial        -> rejected result
admitted command                    -> accepted -> started -> terminal result
```

The terminal transfer status is `completed`, `failed`, `cancelled`, or
`rejected`. Progress samples are emitted while rclone reports statistics.

`cancel` looks up an active target job and cancels its context. `status` reports
whether the target job is active, previously seen, or unknown. These control
actions do not invoke rclone.

## rclone execution boundary

The action-to-subcommand mapping is closed and compiled into the worker:

| TrailTransfer action | rclone subcommand |
|---|---|
| `copy` | `copy` |
| `move` | `move` |
| `sync` | `sync` |
| `check` | `check` |

`internal/rclone.BuildArgs` creates an explicit `[]string`. Its fixed core is:

```text
<subcommand> <source> <target>
--config <rclone-config>
--use-json-log
--stats 500ms
--stats-log-level NOTICE
```

The builder may append only worker-defined options:

- `--max-size <n>M` from policy;
- `--max-depth 1` when `recursive` is explicitly false;
- `--dry-run` from the effective dry-run decision;
- one `--include <pattern>` pair for each validated filter.

Empty filters, filters longer than 256 bytes, filters beginning with `-`, and
filters containing NUL or line-control characters are rejected.

Execution uses:

```go
exec.CommandContext(ctx, rcloneBinary, args...)
```

No shell is involved. Standard output is discarded. Standard error is read as
rclone JSON log lines, bounded error messages are redacted, and statistics are
converted to progress events. Cancelling the context terminates the subprocess.

## Results and evidence

An admitted transfer's terminal result records the job outcome and audit
context, including:

- action, source, and target for executed jobs;
- status, timing, counters, bounded errors, and rclone exit code;
- policy version and policy hash;
- worker version;
- a JCS-based command hash;
- the optional self-declared operator identity and command justification.

Policy and schema rejections with a usable `job_id` also produce result records.
They carry a stable machine-readable reason, an English detail, the policy
version and hash, and a command hash when the rejected payload can be hashed.

The publisher wraps the result payload in an evidence envelope. The envelope
contains a monotonic sequence number, the previous record's hash, recording
time, the payload's `content_hash`, and the envelope's `chain_hash`. Hashing uses
RFC 8785 JSON Canonicalization Scheme semantics implemented in
`internal/evidence`.

When `evidence_journal` is configured, each sealed envelope is appended to a
JSONL file and synchronized to storage. Startup recovers the sequence and
previous hash from the journal. Without a journal, the hash chain exists only
for the process lifetime.

Ed25519 signing is implemented and optional. When `signing_key` is configured,
the sealer signs each envelope's chain hash. `trailtransfer verify` independently
checks journal hashes and can verify signatures when given the corresponding
public key.

MQTT publication is synchronous at the client boundary, but failed publishes
are not queued durably. If sealing fails, the publisher attempts to publish the
unsealed result payload so the terminal outcome is not silently discarded.

The command field `operator` is self-declared. Strong command identity depends
on broker authentication, ACLs, TLS or mTLS, and deployment governance.

## Container architecture

The Docker image is a multi-stage build:

1. `golang:1.26` builds a static TrailTransfer binary.
2. `rclone/rclone:1.68` supplies the rclone executable.
3. `gcr.io/distroless/static-debian12:nonroot` is the runtime image.

The final container runs as `nonroot:nonroot` and contains no shell. The Compose
demo mounts the policy and rclone configuration read-only under `/config`, the
sample source read-only under `/data/input`, and the evidence journal directory
writable under `/data/evidence`.

The demo stack contains TrailTransfer, Mosquitto, MinIO, and a one-shot MinIO
bucket initializer. The worker exposes no inbound HTTP or SSH service.

## Testing and quality gates

The maintained unit tests cover:

- strict command decoding and unknown-field rejection;
- action, source, target, traversal, and sync-policy decisions;
- job completion, rejection, dry-run defaults, and in-process deduplication;
- rclone argument construction and unsafe filter rejection;
- JCS canonicalization and hashing;
- evidence-chain creation, recovery, tamper detection, signing, and signature
  verification.

The standard local gates are:

```bash
gofmt -l ./cmd ./internal
go vet ./...
go test -race ./...
go test -cover ./...
go build ./...
docker compose config -q
make lint
make build
```

The Makefile exposes an `integration-test` entry point, but the repository does
not yet contain a real integration-tagged MQTT/rclone/MinIO test suite. It must
not be reported as integration coverage.

## Archived legacy implementation

The Rust implementation under `legacy/rust/` is retained only as historical
migration context. It is non-production, receives no runtime maintenance, is
not built by Go tooling or CI, and must not be used as deployment guidance. The
maintained product, contracts, examples, container, and documentation describe
the Go implementation above.
