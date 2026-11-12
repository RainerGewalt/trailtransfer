# Worker Policy

The policy (`config/worker-policy.example.yaml`) is the boundary for what the
worker runs. It is operator-authored, kept in version control, and its SHA-256
(`policy_hash`) is stamped into every result event. Defaults are restrictive.

The file is read strictly: an unknown or misspelt key, an action other than
`copy`, `move`, `sync` and `check`, an allowed source or target that breaks
the path rules below, a negative `max_file_size_mb` or a second YAML document
make the policy invalid. `trailtransfer validate-config` then exits 1, and the
worker does not start.

| Field | Meaning | Default posture |
|---|---|---|
| `worker_id` | identifies the worker in its topics | — |
| `policy_version` | human-set version string, echoed in results | required |
| `allowed_actions` | executable actions (`copy/move/sync/check`) | allowlist; everything else rejected |
| `allowed_sources` | permitted read roots (absolute local paths, or `remote:path`) | allowlist |
| `allowed_targets` | permitted rclone remotes/paths (aliases) or, with `allow_absolute_targets`, absolute local paths | allowlist |
| `max_file_size_mb` | passed to rclone `--max-size` in MiB: larger files are skipped, not failed, and not counted; the record carries the limit | limit runaway transfers |
| `max_parallel_jobs` | concurrency bound | excess → `rejected: too_many_jobs` |
| `allow_delete` | permit `move`, which deletes the source after the transfer | **false** — gates `move` even if allowlisted |
| `allow_sync_delete` | permit `sync` to delete on the target | **false** — gates `sync` even if allowlisted |
| `allow_absolute_targets` | permit absolute local target paths | **false** |
| `allow_broadcast_commands` | reserved; broadcast not implemented | **false** |
| `dry_run_default` | run commands without a `dry_run` field as a trial | **true** recommended |
| `require_job_id` | reject commands without `job_id` | **true** |

## Path rules

The same rules apply to the command's `source` and `target` and to the
entries of `allowed_sources` and `allowed_targets`, so both sides of the
comparison are read the same way.

- A value is a remote reference (`name:path`) when the part before the first
  `:` contains no `/`; anything else is a local path, which must be absolute.
- The path is normalised: repeated `/`, `.` segments and a trailing `/` are
  removed. `archive:incoming//./sub/` is the same as `archive:incoming/sub`.
- Refused: a parent segment in any spelling (`..`, `...`, `.. `, look-alike
  dots such as `．．`, and percent-encoded forms such as `%2e%2e`), an encoded
  `/` or `\`, a backslash, a control character, an rclone on-the-fly backend
  (`:sftp,host=…:`), connection-string parameters (`remote,param=value:`), and
  values longer than 4096 bytes. Two dots inside a name, as in
  `report..2026`, are fine.
- A source or target is allowed when it equals an allowlisted entry or sits
  beneath it, compared exactly: the remote name must match, an absolute path
  never matches a relative one, and case counts. `archive:incoming` permits
  `archive:incoming/sub`, not `archive:incoming2`, `archive:Incoming` or
  `archive:/incoming`.
- For local paths, symbolic links are resolved in the command's path and in
  the allowlisted entry, and the resolved path must also stay inside the
  resolved entry. A link that points to a path that does not exist is
  refused. Links behind a remote alias that points to a local folder are not
  resolved, because the worker does not read `rclone.conf`.
- rclone receives the normalised form, and the result records it.

## Evaluation order

1. `job_id` present and 1 to 128 characters from `A-Z a-z 0-9 . _ -`?
   (checked when the command is decoded)
2. `action` ∈ `allowed_actions`?
3. `sync` → `allow_sync_delete`?
4. `move` → `allow_delete`?
5. `source` passes the path rules and lies under an `allowed_sources` entry?
6. local `target` → `allow_absolute_targets`?
7. `target` passes the path rules and lies under an `allowed_targets` entry?

Any failure of a command that carries a valid `job_id` yields a clean
`rejected` **result** event with `reason` and `detail`. A command without a
`job_id`, or with one outside the character set, has no job topic to answer
on: the worker logs it and drops it. Concurrency and duplicate-`job_id` checks
run after policy, at admission.

## Operating notes

- Bump `policy_version` on every change; `policy_hash` changes automatically.
- `sync` is destructive by nature (it makes the target match the source), so it
  is gated behind `allow_sync_delete` even when present in `allowed_actions`;
  `move` deletes the source and is gated behind `allow_delete` the same way.
- Remote **aliases** in `allowed_targets` resolve to `rclone.conf` — the policy
  never contains credentials.
- Run `trailtransfer validate-config` after every change; it fails on the
  first problem it finds.
