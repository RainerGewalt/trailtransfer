# Security Policy (Model)

> Vulnerability reporting, the threat table with the tests behind each line,
> and the known limits are in [`../SECURITY.md`](../SECURITY.md). This page
> lists the rules the code has to keep.

**TrailTransfer must never become a remote shell.** It executes only
allowlisted transfer actions whose parameters passed a local policy. The threat
surface is the **incoming MQTT commands**, treated as untrusted.

## Hard rules

- No `sh -c`, no shell, no arbitrary shell commands.
- No arbitrary rclone args from the MQTT payload — the action→subcommand mapping
  is hard-coded and the command schema is closed (`DisallowUnknownFields`); a
  command is at most 64 KiB.
- `job_id` and `target_job_id` are 1 to 128 characters from
  `A-Z a-z 0-9 . _ -`, because they become MQTT topic levels. A command with
  another `job_id` is dropped and logged, never answered on a topic built from
  it.
- The MQTT message callback never waits for a publish; it only queues the
  command.
- `filters` are validated (no leading `-`, no control chars) before becoming
  `--include` patterns.
- Action / source / target allowlists. Sources and targets are normalised
  and compared exactly; parent segments in any spelling, encoded separators,
  backslashes, on-the-fly backends and connection strings are refused; local
  symbolic links are resolved before the comparison. rclone receives the form
  that was checked.
- `sync` needs `allow_sync_delete`, `move` needs `allow_delete`; both are off
  by default.
- The policy and the config file are read strictly: an unknown key is an
  error.
- `job_id` dedupe while the worker runs (lost on restart, see
  [#1](https://github.com/RainerGewalt/trailtransfer/issues/1)).
- `context` cancellation for `cancel`; the policy's `max_parallel_jobs` bounds
  concurrency.
- Credentials live only in an operator-provided, read-only `rclone.conf`; the
  worker never reads or logs it, and rclone error lines pass a pattern filter
  before they are logged or published.
- A result travels only as a sealed record. If the journal cannot be written,
  nothing is published unsealed, health reports `unhealthy`, and new jobs are
  refused until the worker restarts.
- `policy_version` **and** `policy_hash` in every result; `command_hash` +
  a JCS `content_hash`/`chain_hash` chain make later changes to the journal
  detectable (`trailtransfer verify`).

## Safe rclone execution

```go
// good — argument vector, context, no shell:
exec.CommandContext(ctx, rcloneBinary, "copy", source, target, "--config", cfgPath)

// forbidden:
exec.Command("sh", "-c", userInput)
```

Args are built as a `[]string` from validated fields only. `--dry-run` is set
deliberately from the effective policy/command decision.
