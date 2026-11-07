# Security Model

TrailTransfer moves data in edge/OT/IIoT environments. Its value is **control +
provability**, and its threat surface is precisely the **incoming MQTT
commands**. The design treats them as untrusted.

## Principles

- **MQTT commands are untrusted input.** They are strictly schema-validated
  (unknown fields rejected — there is no free-form args field) and then checked
  against a local policy that is authoritative.
- **No shell execution, ever.** rclone is invoked as an argument vector via
  `exec.CommandContext`, never `sh -c`. The action→subcommand mapping is
  hard-coded; no rclone flags come from the command.
- **The policy is the security boundary.** `worker-policy.yaml` constrains
  actions, source roots, target remotes, file size and parallelism, and gates
  destructive operations.
- **No secrets in logs or events.** Backend credentials live only in an
  operator-provided, read-only `rclone.conf`. Remotes are referenced by alias.
  Error output is redacted.
- **Least privilege.** The container runs non-root on a distroless static base;
  `/config` is mounted read-only; there is no inbound port on the edge — only one
  outbound MQTT connection.

## Threats & mitigations

| Threat | Mitigation |
|---|---|
| Malicious MQTT command | strict schema (`DisallowUnknownFields`) + policy engine |
| Path traversal | `filepath.Clean` + prefix check against `allowed_sources`; `..` rejected |
| Command injection | argv vector, no shell, no free-form flag field; filter patterns validated |
| Secret leakage | redaction, remote aliases, `rclone.conf` never logged |
| Destructive sync/delete | `allow_delete` / `allow_sync_delete` default **off**; `dry_run_default` |
| Replayed job_id | `job_id` dedupe (idempotency set) — safe under QoS-1 redelivery |
| Uncontrolled parallelism | `max_parallel_jobs` → `rejected: too_many_jobs` |
| Large-file overload | `max_file_size_mb` / rclone `--max-size` |

## Deployment hardening

Use broker authentication and ACLs so only authorised senders can publish to
`…/commands`; enable TLS/mTLS on the broker; pin the rclone version (it is
reported in `capabilities`/`result`). Broadcast commands
(`trailtransfer/all/commands`) are **not** implemented and off by design.

## Reporting a vulnerability

Please do not report a suspected vulnerability in a public issue, pull
request or discussion.

Report it privately through GitHub instead: open the **Security** tab of this
repository and choose **Report a vulnerability**, or go straight to
<https://github.com/RainerGewalt/trailtransfer/security/advisories/new>. Only you
and the maintainer can see the report and the conversation about it.

Include what you can safely share: the affected version or commit, how to
reproduce it, and the impact as you understand it. Never include real
credentials, private keys or production data.

## What happens after you report

- Reports are handled confidentially. Nothing from a report is copied into a
  public issue. If a report is confirmed, it becomes public as a GitHub
  security advisory together with the fix, crediting you if you want that.
- Reports are read and answered on a best-effort basis by a single
  maintainer. There is no guaranteed response or fix time, and none is
  implied by this document.
- A report that includes a reproduction is acted on before one that does not.
- Fixes ship in the next release. There is no backport to older releases.
