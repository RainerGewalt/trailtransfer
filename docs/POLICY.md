# Worker Policy

The policy (`config/worker-policy.example.yaml`) is the security boundary. It is
operator-authored, kept in version control, and its SHA-256 (`policy_hash`) is
stamped into every result event. Defaults are restrictive.

| Field | Meaning | Default posture |
|---|---|---|
| `worker_id` | identifies the worker in its topics | — |
| `policy_version` | human-set version string, echoed in results | required |
| `allowed_actions` | executable actions (`copy/move/sync/check`) | allowlist; everything else rejected |
| `allowed_sources` | permitted read roots (prefix match, no `..`) | allowlist |
| `allowed_targets` | permitted rclone remotes/paths (aliases) | allowlist |
| `max_file_size_mb` | passed to rclone `--max-size` | limit runaway transfers |
| `max_parallel_jobs` | concurrency bound | excess → `rejected: too_many_jobs` |
| `allow_delete` | permit delete/purge actions | **false** |
| `allow_sync_delete` | permit `sync` to delete on the target | **false** — gates `sync` even if allowlisted |
| `allow_absolute_targets` | permit absolute local target paths | **false** |
| `allow_broadcast_commands` | reserved; broadcast not implemented | **false** |
| `dry_run_default` | run commands without a `dry_run` field as a trial | **true** recommended |
| `require_job_id` | reject commands without `job_id` | **true** |

## Evaluation order

1. `require_job_id` → `job_id` present?
2. `action` ∈ `allowed_actions`?
3. `sync` → `allow_sync_delete`?
4. `source` under an `allowed_sources` root (after `filepath.Clean`, no `..`)?
5. absolute local `target` → `allow_absolute_targets`?
6. `target` in/under `allowed_targets`?

Any failure of a command that carries a `job_id` yields a clean `rejected`
**result** event with `reason` and `detail`. A command without a `job_id`
(with `require_job_id: true`) has no job topic to answer on: the worker logs
it and drops it. Concurrency and duplicate-`job_id` checks run
after policy, at admission.

## Operating notes

- Bump `policy_version` on every change; `policy_hash` changes automatically.
- `sync` is destructive by nature (it makes the target match the source), so it
  is gated behind `allow_sync_delete` even when present in `allowed_actions`.
- Remote **aliases** in `allowed_targets` resolve to `rclone.conf` — the policy
  never contains credentials.
