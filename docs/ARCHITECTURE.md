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
status, progress, logs, and sealed result record
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
| `internal/evidence` | JCS hashing, Trail Evidence Record v1 sealing, hash chaining, journaling, signing, and verification |
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
  "target": "s3-demo:trailtransfer",
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
`DisallowUnknownFields`. It rejects payloads above 64 KiB, requires a `job_id`
of 1 to 128 characters from `A-Z a-z 0-9 . _ -` (it becomes a topic level),
rejects unknown actions, and requires a `target_job_id` from the same
character set for control actions. There is deliberately
no `args`, `flags`, or equivalent pass-through field. The published JSON Schema
under `schemas/` documents the external contract; runtime validation is also
implemented in Go and does not execute the schema document dynamically.

`recursive` and `dry_run` are pointer fields internally so the worker can
distinguish an omitted value from an explicit `false`. An omitted `dry_run`
uses the policy's `dry_run_default`. Filter values become individual rclone
`--include` arguments after validation.

## Current policy boundary

The worker policy is loaded from local YAML with unknown keys rejected. MQTT
commands cannot change its allowlists or limits. The active checks are:

- the action must appear in `allowed_actions`;
- the source must equal or sit beneath one of the `allowed_sources` roots;
- the target must equal or sit beneath one of the `allowed_targets` entries;
- an absolute local target additionally requires `allow_absolute_targets`;
- `sync` additionally requires `allow_sync_delete`, because it can delete data
  from the target;
- `move` additionally requires `allow_delete`, because it deletes the source;
- admitted jobs are bounded by `max_parallel_jobs`;
- `max_file_size_mb` becomes rclone's `--max-size` argument;
- an omitted command-level `dry_run` uses `dry_run_default`.

`copy` and `check` are non-destructive. There is no generic delete or purge
command.

`internal/policy/paths.go` parses every source, target and allowlist entry
into a remote name and a path, normalises the path (repeated `/`, `.`
segments, trailing `/`), and refuses parent segments in any spelling,
encoded separators, backslashes, control characters, on-the-fly backends and
connection-string parameters. The comparison is exact and separator-aware, so
`/data/input-other` is not a child of `/data/input`. For local paths it also
resolves symbolic links in the value and in the root and repeats the
comparison on the resolved paths. Remote paths are not resolved further: the
policy engine does not read `rclone.conf`. The normalised form is what the
manager hands to rclone and what the result records; `docs/POLICY.md` has the
rules.

Each loaded policy receives:

- `policy_version`, set by the operator; and
- `policy_hash`, calculated from the exact policy file bytes.

Both values are included in terminal transfer results.

## Job admission and lifecycle

`internal/jobs.Manager` owns the execution state:

- a bounded queue (256 commands) between the MQTT message callback and a
  single goroutine that handles commands in arrival order, so the callback
  never waits for a publish;
- `seen` records admitted job IDs and rejects a repeated ID as
  `duplicate_job_id`;
- `outcome` holds each handled job's last known status for status queries;
- `running` enforces the policy's `max_parallel_jobs` limit;
- `active` maps running job IDs to `context.CancelFunc` values;
- a mutex protects these structures.

Without a journal this state lives only as long as the process. With a
journal it survives a restart, see "Restart and recovery" below.

For an executable command, the manager follows this sequence:

```text
decode failure with a usable job_id -> rejected result
policy denial                       -> rejected result
duplicate or capacity denial        -> rejected result
admitted command                    -> accepted -> started -> terminal result
```

The terminal transfer status is `completed`, `failed`, `cancelled`, or
`rejected`. Progress samples are emitted while rclone reports statistics.

`cancel` looks up an active target job and cancels its context. `status`
answers on the query's own topic with the target job's last known status
(`target_status`). These control actions do not invoke rclone and are not
sealed.

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

The publisher seals the result payload as a Trail Evidence Record v1
(`trail.evidence/1`). The record contains the producer, a stream id, a
monotonic sequence number, the previous record's hash, the recording time and
its source, the actor, the governing policy, the payload's `content_hash`, and
the record's `chain_hash`. Hashing uses RFC 8785 JSON Canonicalization Scheme
semantics implemented in `internal/evidence`. The format is described in
[`EVIDENCE_MODEL.md`](EVIDENCE_MODEL.md).

When `evidence_journal` is configured, each sealed record is appended to a
JSONL file and synchronized to storage. Startup reads the last record of the
journal and seals the first new record with `chain_restart` and `anchor_hash`
pointing at it; without a journal, or with an empty one, the anchor is the
genesis hash. A journal whose last line is unreadable stops the worker.

Ed25519 signing is implemented and optional. When `signing_key` is configured,
the sealer signs the ASCII bytes of each record's `chain_hash`. `trailtransfer
verify` independently checks schema, hashes, links, sequence and restarts, and
can verify signatures when given the corresponding public key.

A result is published only as a sealed record. The first failed journal
write stops the sealer for the rest of the process: the error is logged,
nothing is published unsealed, health turns `unhealthy`, and the manager
refuses every new job until the worker is restarted.

## Restart and recovery

Everything in this section needs `evidence_journal`. Without a journal the
chain, the job IDs and the outcomes are in memory and end with the process.

**Order before a transfer.** A command is decoded, checked by the policy and
admitted. The job is then written to the state file `<journal>.jobs`
(temporary file, `fsync`, rename, `fsync` of the directory), and only after
that write has succeeded does the worker start rclone. If the state cannot
be written, the job is refused as `state_unavailable` and rclone does not
run. There is no moment at which rclone runs for a job the disk does not
know. The reverse can happen: a worker that dies after the write and before
rclone starts leaves a job in the state file that never ran; it is sealed
as interrupted like any other, because the worker cannot tell the two apart.

**After a start**, before the worker subscribes to commands:

1. The journal is read once. Every `job_id` with a `transfer.job.result` or
   `transfer.job.interrupted` record is refused again as `duplicate_job_id`,
   and the detail names the stored status. Outcomes, including rejections,
   answer status queries. A `job_id` that was only rejected can be sent
   again, as in a running worker.
2. Each job in the state file that has no outcome in the journal is sealed
   as `transfer.job.interrupted` with `reason: worker_restart`. A job leaves
   the state file only after its record is in the journal, and a job with a
   record in the journal is never sealed again, so a crash at any point of
   this step still ends in exactly one record.
3. No job is resumed and none is run again. Whoever sent it decides, after a
   look at the target, whether to send it under a new `job_id`.

**On SIGTERM or SIGINT** the worker admits nothing new (a command that still
arrives is rejected as `worker_stopping`), ends every running rclone, seals
each of those jobs as `transfer.job.interrupted` with
`reason: worker_shutdown`, and waits up to 15 s for that. What is not sealed
in time is sealed by the next start.

**A killed worker and rclone.** On Linux the kernel ends rclone when the
worker dies (`PR_SET_PDEATHSIG`), so a job is not sealed as interrupted while
its rclone still writes. On other systems a killed worker can leave rclone
running.

**Publishing.** Sealed records are published from the journal, in journal
order, by an outbox that keeps its position in `<journal>.outbox`. The
position moves only past records the broker has confirmed. A record sealed
while the broker is away, or shortly before the worker stops, is published
after the next connection, also by a later process. Delivery is at least
once: a record can arrive twice, as the same bytes with the same `seq` and
`chain_hash`, and consumers deduplicate on those. A worker that gets an
outbox for the first time, or whose position file is missing or does not
fit the journal, starts at the end of the journal and does not publish its
history again. Status, progress and log messages are not queued: without a
connection they are dropped, because they describe a moment that has passed.

**What this does not cover.**

- An interrupted record says that the worker did not see the job to its
  end. It does not say which files rclone had already written, moved or
  deleted; that needs the per-file manifest of
  [#2](https://github.com/RainerGewalt/trailtransfer/issues/2) or a look at
  the target.
- A journal whose last line is cut off (for example after a power loss
  during a write) stops the worker at the start, and so does a state file
  that cannot be read. Nothing is repaired automatically. Keep a copy of the
  journal, remove the incomplete last line or move the state file away, and
  start again; the next record is a restart that `verify` reports.
- The journal is read completely at every start, and the set of known
  `job_id`s grows with it. Rotating the journal away also removes its
  `job_id`s from the duplicate check.
- One worker process per journal. On Linux and macOS a second worker on
  the same journal does not start (`<journal>.lock`); on Windows two
  processes on the same files are not detected.
- The answer to a repeated `job_id` is a new `transfer.job.rejected` record
  that names the stored status. The stored record itself is not published
  again; it is in the journal under the same `job_id`.
- Execution attempts have no identifier of their own, because a `job_id` is
  executed at most once.

The command field `operator` is self-declared. Strong command identity depends
on broker authentication, ACLs and deployment governance. The client connects
over plain TCP; TLS is not supported.

## Container architecture

The Docker image is a multi-stage build:

1. `golang:1.26` builds a static TrailTransfer binary.
2. `rclone/rclone:1.68` supplies the rclone executable.
3. `gcr.io/distroless/static-debian12:nonroot` is the runtime image.

The final container runs as `nonroot:nonroot` and contains no shell. The Compose
demo mounts the policy and rclone configuration read-only under `/config`, the
sample source read-only under `/data/input`, and the evidence journal directory
writable under `/data/evidence`.

The demo stack contains TrailTransfer, Mosquitto, an S3 target served by
`rclone serve s3` from the pinned rclone image, and a one-shot step that
creates the journal file writable for everyone, so the nonroot worker can
append to a bind-mounted directory and the host can read it. That step is
demo-only. The worker exposes no inbound HTTP or SSH service.

## Testing and quality gates

The maintained unit tests cover:

- strict command decoding, unknown-field rejection and the `job_id` character
  set;
- action, source, target, path normalisation, symbolic links, traversal in
  any spelling, and the sync and move guards; strict policy and config
  loading;
- job completion, rejection, dry-run records, size-limit notes, status
  queries, a journal write failure, the non-blocking command queue,
  deduplication, and the restart cases: a killed worker, a shutdown during a
  transfer, an outcome in the journal with a stale state file, a state file
  that cannot be written, and the outbox across an outage and a restart;
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

`make negative-paths` runs the edge cases of the policy and the journal
against a real Mosquitto and rclone without Docker, and `make first-use` runs
the documented Quickstart on the Docker demo; both are run by hand before a
release and are described in [`TESTING.md`](TESTING.md). `negative-paths`
includes a worker killed during a transfer, a worker stopped during a
transfer and a restart during a broker outage (checks R1 to R3). Neither
covers SFTP, a real S3 service, or a power loss.

## Archived legacy implementation

The Rust implementation under `legacy/rust/` is retained only as historical
migration context. It is non-production, receives no runtime maintenance, is
not built by Go tooling or CI, and must not be used as deployment guidance. The
maintained product, contracts, examples, container, and documentation describe
the Go implementation above.
