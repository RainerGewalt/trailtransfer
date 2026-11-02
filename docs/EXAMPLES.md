# Examples

Bring up the demo stack, subscribe to the worker, and publish commands:

```bash
docker compose up --build
mosquitto_sub -h localhost -p 1883 -v -t 'trailtransfer/worker-01/#'
mosquitto_pub  -h localhost -p 1883 -t trailtransfer/worker-01/commands \
  -f examples/commands/copy.json
```

## Command files (`examples/commands/`)

| File | What it shows |
|---|---|
| `copy.json` | a normal `copy` job to MinIO with filters |
| `check.json` | a non-destructive `check` (source vs. target) |
| `cancel.json` | cancelling a running job by `target_job_id` |
| `sync-dry-run.json` | a **policy rejection** — `sync` is gated by `allow_sync_delete` (default `false`) |

## Expected lifecycle (valid copy)

```
jobs/job-001/status   {"status":"accepted"}
jobs/job-001/status   {"status":"started"}
jobs/job-001/logs     {"level":"INFO","message":"starting copy /data/input -> minio-demo:trailtransfer (dry_run=true)"}
jobs/job-001/result   {"schema":"trail.evidence/1","seq":7,"prev_hash":"sha256:…","event_type":"transfer.job.result",...,"payload":{"status":"completed",...,"command_hash":"sha256:…"},"content_hash":"sha256:…","chain_hash":"sha256:…","job_id":"job-001"}
jobs/job-001/status   {"status":"completed"}
```

## Rejections

Sending a disallowed source produces a clean rejection (no rclone runs):

```json
{ "event_type": "result", "job_id": "job-002", "status": "rejected",
  "reason": "policy_violation",
  "detail": "source \"/etc\" is not under any allowed_sources root",
  "policy_version": "1", "policy_hash": "sha256:…", "command_hash": "sha256:…" }
```

Sending an unknown field (an attempt to smuggle rclone args) is rejected by the
schema:

```json
{ "event_type": "result", "job_id": "job-003", "status": "rejected",
  "reason": "schema_violation",
  "detail": "schema: json: unknown field \"extra_args\"" }
```

## Completed result event

See [`../examples/events/result.completed.json`](../examples/events/result.completed.json).
