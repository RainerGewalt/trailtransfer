<p align="center">
  <img src="docs/brand/trailtransfer-mark.svg" width="96"
       alt="TrailTransfer logo: a transfer arrow between two records." />
</p>

<h1 align="center">TrailTransfer</h1>

**MQTT-controlled rclone worker for audit-ready edge file transfers.**

[![CI](https://github.com/RainerGewalt/trailtransfer/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/RainerGewalt/trailtransfer/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/RainerGewalt/trailtransfer.svg)](https://pkg.go.dev/github.com/RainerGewalt/trailtransfer)
[![GHCR image](https://img.shields.io/badge/ghcr.io-trailtransfer-1f6feb?logo=docker&logoColor=white)](https://github.com/RainerGewalt/trailtransfer/pkgs/container/trailtransfer)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

TrailTransfer lets edge machines execute file-transfer jobs without exposing an
HTTP API, opening SSH, or relying on invisible cron scripts. Jobs arrive over
**MQTT**, are validated against a local **policy**, executed through **rclone**,
and reported back as structured **status / progress / result / evidence** events.

TrailTransfer does **not** replace rclone — rclone stays the transfer engine.
TrailTransfer is the MQTT control plane, policy layer, job manager and evidence
wrapper around it.

> Works with any MQTT broker. TrailMQ can add fleet governance, contracts and
> centralized evidence — but is never required.

```
MQTT command in
  → policy validation
  → controlled rclone execution (argv, never a shell)
  → progress / status / result out
  → sealed, hash-chained evidence record  →  trailtransfer verify
```

Every finished job (success **or** rejection) is sealed into a tamper-evident,
append-only journal and can be **independently verified** — no database, no
cloud, no TrailMQ. This is what makes it *audit-ready* and a building block for
ALCOA++/GxP-style data integrity (see [`docs/EVIDENCE_MODEL.md`](docs/EVIDENCE_MODEL.md)
and [`docs/COMPLIANCE.md`](docs/COMPLIANCE.md)).

![Two terminal transcripts. Terminal 1 runs mosquitto_sub on the job status topics and shows job-001 going from accepted to started to completed. Terminal 2 sends examples/commands/copy.json with mosquitto_pub; jq shows the journal record with seq 0, event_type transfer.job.result, status completed, 3 files, 11887 bytes and rclone exit code 0; docker compose exec s3 ls lists sample-01.png, sample-02.png and sample-03.jpg in the demo bucket; ./trailtransfer verify prints OK for 1 record.](docs/images/copy-job-transcript.png)

*The Quickstart demo below, with three sample files in `./upload_folder`:
the job runs through `accepted`, `started` and `completed`, its result is
sealed in the journal, the files are in the demo S3 bucket, and
`trailtransfer verify` checks the journal on the host.*

## What it is / is not

**It is:** an MQTT-controlled edge transfer worker · a policy-guarded rclone
runner · an audit-ready job executor · a TrailMQ-native worker.

**It is not:** a replacement for rclone · a remote shell · an unrestricted file
manager · an enterprise MFT suite · a cloud service.

## Why not just cron + rclone, or SSH?

- **vs. cron + rclone:** cron scripts run silently, accept whatever flags the
  script author wrote, and leave no trustworthy record of *what actually ran*.
  TrailTransfer validates every job against a local policy, forbids arbitrary
  rclone flags, and seals each outcome — success **or** rejection — into a
  tamper-evident, independently verifiable evidence record.
- **vs. SSH / remote shell:** a shell on an edge box is unlimited authority.
  TrailTransfer exposes **no inbound port** and **no shell**; it holds a single
  outbound MQTT connection and only ever runs a fixed, allowlisted rclone action
  built as an argument vector — never a shell string.
- **What you can prove:** for every job — which action, with which policy
  (`policy_hash`), from where to where, its rclone exit code and counters,
  chained to the previous record. Per-file content manifests and trusted
  timestamps remain future assurance work; see
  [the evidence model](docs/EVIDENCE_MODEL.md) for the current boundary.

## Install

- **Container image** for linux/amd64 and linux/arm64, with rclone 1.68
  inside, running as `nonroot`:
  `docker pull ghcr.io/rainergewalt/trailtransfer:0.1.0`. Mount config,
  policy and `rclone.conf` under `/config` as `docker-compose.yaml` does.
- **Go 1.26 or newer:**
  `go install github.com/RainerGewalt/trailtransfer/cmd/trailtransfer@latest`.
  Transfer jobs need `rclone` on the `PATH`; without it the worker starts,
  reports `rclone_available: false` and fails transfer jobs. A binary built
  this way from v0.1.0 reports its version as `dev`; main fixes that.
- **From a clone:** `make build`, or `go build ./cmd/trailtransfer`.

The GitHub release has no prebuilt binaries yet.

## Quickstart

```bash
docker compose up --build
```

In another terminal, watch everything the worker publishes, then send a command:

```bash
mosquitto_sub -h localhost -p 1883 -v -t 'trailtransfer/worker-01/#'

mosquitto_pub  -h localhost -p 1883 \
  -t trailtransfer/worker-01/commands \
  -f examples/commands/check.json
```

You'll see `accepted → started → … → completed` on the job's topics, ending in a
sealed **evidence record** (`seq`, `prev_hash`, `content_hash`, `chain_hash`)
whose `payload` carries `command_hash`, `policy_version`, `policy_hash` and
`worker_version`.

> `examples/commands/copy.json` copies the `.jpg` and `.png` files from
> `./upload_folder` (mounted read-only as `/data/input`; put a few files there
> first) to the demo S3 target `s3-demo:trailtransfer`. That target is rclone's
> own S3 server (`rclone serve s3`) from the same pinned rclone release the
> worker uses; rclone creates the bucket on the first copy, and
> `docker compose exec s3 ls -l /data/trailtransfer` lists what arrived.
> `examples/commands/sync-dry-run.json` is intentionally
> **rejected** by the demo policy — `sync` can delete on the target, so it is
> gated behind `allow_sync_delete` (default `false`) — to show what a policy
> denial looks like.

## Example command

```json
{
  "job_id": "job-001",
  "action": "copy",
  "source": "/data/input",
  "target": "s3-demo:trailtransfer",
  "recursive": true,
  "filters": ["*.jpg", "*.png"],
  "dry_run": false
}
```

Actions: `copy` · `move` · `sync` · `check` · `cancel` · `status`. There is
deliberately **no** `args`/`flags`/`extra` field — unknown fields are rejected.

## Example result event (sealed evidence record)

The result is sealed as a **Trail Evidence Record v1** (`trail.evidence/1`),
the evidence format shared by the Trail projects. Its reference specification
and cross-implementation test vectors are maintained in the sister project
[trail-evidence](https://github.com/RainerGewalt/trail-evidence), whose
verifiers also check TrailTransfer journals;
[`docs/EVIDENCE_MODEL.md`](docs/EVIDENCE_MODEL.md) describes the format as
implemented here, [`schemas/`](schemas/) holds the JSON Schemas, and
[`examples/events/result.completed.json`](examples/events/result.completed.json)
a full sample. Each record is chained to the previous one:

```json
{
  "schema": "trail.evidence/1",
  "producer": { "kind": "transfer-worker", "id": "worker-01", "version": "0.1.0" },
  "stream_id": "worker-01",
  "seq": 7,
  "prev_hash": "sha256:…",
  "event_type": "transfer.job.result",
  "time": { "recorded_at": "2026-11-02T10:00:12.500Z", "source": "system" },
  "actor": { "auth": "self-declared", "id": "alice" },
  "policy": { "id": "worker-policy.yaml", "version": "1", "hash": "sha256:…", "kind": "transfer-policy" },
  "payload": {
    "action": "copy", "source": "/data/input", "target": "s3-demo:trailtransfer",
    "status": "completed", "files_transferred": 10, "bytes_transferred": 12345678,
    "rclone_exit_code": 0, "policy_version": "1", "policy_hash": "sha256:…",
    "worker_version": "0.1.0", "command_hash": "sha256:…"
  },
  "content_hash": "sha256:…",
  "chain_hash": "sha256:…",
  "job_id": "job-001"
}
```

`content_hash` is SHA-256 over the RFC 8785 (JCS) form of `payload`;
`chain_hash` is SHA-256 over the JCS form of the record core and links to
`prev_hash`. Rejected jobs are sealed as `transfer.job.rejected`. The first
record after every worker start carries `chain_restart` and `anchor_hash`, so
restarts are visible without being chain breaks.

Verify a whole journal independently: `trailtransfer verify /data/evidence/journal.jsonl`.
It reports `hash_mismatch`, `chain_break`, `seq_gap`, `schema_invalid`,
`signature_invalid` and (informational) `restart` findings per record.

## Worker policy — the security boundary

MQTT commands are untrusted input; `config/worker-policy.example.yaml` is
authoritative. Every command is checked before any rclone process is built, and
the policy's SHA-256 (`policy_hash`) is stamped into every result event.

```yaml
worker_id: worker-01
policy_version: "1"
allowed_actions: [copy, move, sync, check]
allowed_sources: [/data/input, /data/reports]
allowed_targets: [sftp-demo:/upload, s3-demo:trailtransfer]
max_file_size_mb: 500
max_parallel_jobs: 2
allow_delete: false
allow_sync_delete: false        # sync is allowlisted but still gated by this
allow_absolute_targets: false
allow_broadcast_commands: false
dry_run_default: true
require_job_id: true
```

Remote **aliases** (`sftp-demo:`) resolve to `rclone.conf`, which the operator
provides and mounts read-only. TrailTransfer never generates it and never logs
it. See [`docs/POLICY.md`](docs/POLICY.md) for every field.

![Terminal transcript. mosquitto_pub sends examples/commands/sync-dry-run.json. jq shows the journal record: event_type transfer.job.rejected, status rejected, detail "sync (which may delete on the target) is not permitted: allow_sync_delete is false", a chain_hash and an ed25519 signature. trailtransfer verify with the public key prints OK, 1 record, 1 signature verified. After sed changes the status to completed in a copy, trailtransfer verify prints FAIL with hash_mismatch and exits with 1.](docs/images/policy-rejection-transcript.png)

*A local run with this policy and a signing key (`trailtransfer keygen`,
`TRAILTRANSFER_SIGNING_KEY`): the policy refuses a `sync` command, the
refusal is sealed and signed in the journal, and `trailtransfer verify`
passes the journal but fails an edited copy.*

## MQTT topics

| Topic (`{w}` = worker_id, `{j}` = job_id) | Dir | QoS | Retained |
|---|---|---|---|
| `trailtransfer/{w}/commands` | in | 1 | – |
| `trailtransfer/{w}/jobs/{j}/status` | out | 1 | – |
| `trailtransfer/{w}/jobs/{j}/progress` | out | 0 | – |
| `trailtransfer/{w}/jobs/{j}/result` | out | 1 | – |
| `trailtransfer/{w}/jobs/{j}/logs` | out | 0 | – |
| `trailtransfer/{w}/health` | out | 1 | ✓ (+ LWT `offline`) |
| `trailtransfer/{w}/capabilities` | out | 1 | ✓ |

Job lifecycle: `received → rejected` (schema/policy) **or**
`received → accepted → started → progress* → completed | failed | cancelled`.

## CLI

```bash
trailtransfer run [--config config.yaml]     # start the worker
trailtransfer validate-config [--config …]   # load config+policy, exit 0/1
trailtransfer print-capabilities [--config …]# print the capabilities JSON
trailtransfer verify [--pubkey key.pub] <journal>  # independently verify the evidence chain and, with a key, the signatures
trailtransfer keygen [--out name]            # generate an Ed25519 signing key pair
trailtransfer version
```

## Configuration

A YAML config is optional; `TRAILTRANSFER_*` env vars override it (see
`.env.example`). Required: `worker_id`, `mqtt.host`, `policy_file`,
`rclone_config`.

## Security model

MQTT commands are untrusted; the local policy is authoritative. No shell
execution, no arbitrary rclone flags, no secrets in logs, no unrestricted
paths/remotes. Duplicate `job_id`s are deduplicated; `max_parallel_jobs` bounds
concurrency; a JCS hash chain (`content_hash`/`chain_hash`) backs a verifiable audit trail. See
[`SECURITY.md`](SECURITY.md) and [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

## TrailMQ integration

TrailTransfer executes controlled file jobs; TrailMQ governs and proves them —
registering the worker as an expected system, managing pub/sub rights, capturing
result events as evidence, and rendering transfers on a timeline. It's purely
additive: TrailTransfer runs against any broker on its own.

```
TrailSource   → emits context-rich source events
TrailTransfer → moves files as controlled jobs
TrailMQ       → governs & proves both
```

## Build & test

```bash
go build ./cmd/trailtransfer
go test ./...
```

## Roadmap

- **v0.1.0** *(released 2026-11-02)* — MQTT worker, `copy/move/sync/check` +
  `cancel/status`, policy validation, safe rclone execution, status/result
  events, health/capabilities, Docker Compose demo; evidence as Trail Evidence
  Record v1 (RFC 8785 hashing, `seq`/`prev_hash` **hash chain**, restart
  anchoring, append-only journal, `trailtransfer verify`, JSON Schemas) and
  optional **Ed25519 signatures** (`keygen`, `verify --pubkey`).
- **0.2.0** ([milestone](https://github.com/RainerGewalt/trailtransfer/milestone/1))
  — survive crashes with durable job state, deduplication and an MQTT outbox
  (#1); a per-file transfer manifest as its own evidence record (#2); a
  public end-to-end demo between simulated systems with verification (#3);
  format 0.2 of trail-evidence (#4).
- **later** — RFC 3161 timestamps, WORM export, retries.
- **v1.0** — stable schemas, hardened image, full CI, deployment/validation guide.

## Contributing

Contributions are welcome within TrailTransfer's deliberately narrow scope. See
[`CONTRIBUTING.md`](CONTRIBUTING.md) for local setup, the hard security rules and
commit conventions, [`CHANGELOG.md`](CHANGELOG.md) for the release history, and
[`docs/`](docs/) for the architecture, policy, MQTT contract and evidence model.
Report vulnerabilities privately per [`SECURITY.md`](SECURITY.md).

## Feedback and support

Tried it? An
[evaluation report](https://github.com/RainerGewalt/trailtransfer/issues/new?template=evaluation-report.yml)
helps more than anything else: what you ran, whether it worked, and what got
in the way. A first run that failed is as useful as one that worked. Bugs can
also go into a plain issue. Vulnerabilities go to [SECURITY.md](SECURITY.md), not to a
public issue.

Support is best effort: issues are read and answered, but there is no
guaranteed response time, no support contract, no SLA and no paid support.

## License

Apache-2.0 (see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE)). rclone is invoked
as a separate MIT-licensed subprocess and is not a derived work.
