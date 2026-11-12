# Security Model

TrailTransfer moves files on edge and IIoT systems on commands it receives
over MQTT. Those commands are its threat surface, and the design treats them
as untrusted. This page says what the worker does about each threat, which
test shows it, and where the protection ends. It describes the current
`main`; the published 0.1.1 lacks the protections marked as new in
[`CHANGELOG.md`](CHANGELOG.md) under "Unreleased".

## Who can send commands

Anyone who can publish to `trailtransfer/{worker_id}/commands` on the broker
can ask the worker for any transfer the policy allows, and can try every
input the policy has to refuse. The worker cannot tell senders apart: the
`operator` field is self-declared. Restricting who may publish to the command
topic, with broker authentication and ACLs, is therefore the first control
and the operator's responsibility. The controls below limit what an allowed
or a compromised sender can do.

## Principles

- **MQTT commands are untrusted input.** They are decoded with a closed
  schema (unknown fields are rejected, there is no free-form args field, a
  command is at most 64 KiB) and then checked against a local policy that is
  authoritative.
- **No shell execution.** rclone is invoked as an argument vector via
  `exec.CommandContext`, never `sh -c`. The action-to-subcommand mapping is
  fixed; no rclone flags come from the command.
- **The policy is the boundary for what runs.** `worker-policy.yaml` limits
  actions, source roots, target remotes and paths, file size and parallelism,
  and gates the actions that delete (`sync`, `move`).
- **Credentials stay in `rclone.conf`.** The operator provides it read-only;
  commands and events refer to remotes by alias, and the worker never reads or
  logs the file. rclone error lines are passed through a pattern filter before
  they are logged or published (limits below).
- **Least privilege.** The container runs as `nonroot` on a distroless static
  base; `/config` is mounted read-only; the worker opens no inbound port, only
  one outbound MQTT connection.

## Threats, what the worker does, and what shows it

| Threat | What the worker does | Shown by |
|---|---|---|
| Malicious MQTT command | closed schema, at most 64 KiB, then the policy, before any rclone process is built | `internal/commands/decode_test.go`, `internal/policy/*_test.go` |
| A command that takes the worker off the broker | `job_id` and `target_job_id` must be 1 to 128 characters from `A-Z a-z 0-9 . _ -`, so no answer goes to an invalid topic; a command with another `job_id` is logged and dropped. The MQTT callback only queues commands and never waits for a publish | `TestDecodeRejectsJobIDsThatAreNotASafeTopicLevel`, `TestInvalidJobIDsArePublishedNowhere`, `TestHandleNeverWaitsForAPublish`; `make negative-paths`, check E1 |
| Path traversal and other spellings of a path | sources and targets are normalised (repeated `/`, `.` segments and a trailing `/` removed) and must equal or sit beneath an allowlisted entry; parent segments in any spelling (`..`, `...`, `.. `, look-alike dots, percent-encoded), encoded separators, backslashes, control characters, rclone on-the-fly backends (`:backend:`) and connection-string parameters (`remote,param=value:`) are refused; for local paths symbolic links are resolved and the resolved path must stay inside the resolved root; rclone receives the normalised form that was checked | `internal/policy/boundary_test.go`; `make negative-paths`, checks S1 |
| Command injection | argument vector, no shell, no free-form flag field; filter patterns may not start with `-` or carry control characters | `internal/rclone/argv_test.go`, `TestDecodeRejectsUnknownField` |
| Secret leakage | `rclone.conf` is never read or logged; remotes are referenced by alias; rclone error lines are masked by a pattern filter | `internal/logging/redact_test.go` (masked forms); limits below |
| Destructive actions | `sync` needs `allow_sync_delete: true`, `move` needs `allow_delete: true`, both default `false`; `dry_run_default: true` makes a command without `dry_run` a trial | `TestSyncNeedsAllowSyncDelete`, `TestEvaluateRefusesMoveWhileAllowDeleteIsFalse`, `TestDryRunDefaultApplied`; `make negative-paths`, check S2 |
| A policy typo that switches a guard off | an unknown or misspelt key, or an action other than `copy`, `move`, `sync`, `check`, makes the policy invalid: `validate-config` fails and the worker does not start; the same applies to the config file | `internal/policy/policy_test.go`, `internal/config/config_test.go`, `TestValidateConfigFailsOnAPolicyTypo`; `make negative-paths`, check S4 |
| Replayed `job_id` | refused as `duplicate_job_id` while the worker runs; the set is lost on restart (limits below) | `TestHandleDedupesJobID` |
| Uncontrolled parallelism | the policy's `max_parallel_jobs`; a job beyond it is `rejected: too_many_jobs` | `TestParallelJobsAreBoundedByThePolicy` |
| Large files | `max_file_size_mb` is passed to rclone as `--max-size`; larger files are skipped, not failed, and not counted; the record carries `max_file_size_mb` and the status says that larger files were excluded | `TestBuildArgsCopy`, `TestSizeLimitIsNamedInTheRecord`; `make negative-paths`, check E7 |
| Evidence lost on a journal write failure | the first failed write stops sealing for the rest of the process; no result is published unsealed; the error is logged, health reports `unhealthy` and new jobs are refused until the worker is restarted | `TestSealerSealsNothingAfterAJournalWriteFailure`, `TestJournalWriteFailureStopsSealingAndRefusesNewJobs`; `make negative-paths`, check E6 |

## Known limits

- **No TLS to the broker.** The worker connects over plain TCP
  (`tcp://host:port`). User name, password, commands and results cross the
  network unencrypted, and a broker that offers only TLS cannot be reached.
  Run the broker on the same host or in the same trusted network segment, or
  reach a remote broker through a tunnel (for example WireGuard or SSH port
  forwarding).
- **Deduplication only within one process.** A `job_id` that was handled is
  refused again until the worker restarts; after a restart the same command
  runs again. Durable job state is planned in
  [#1](https://github.com/RainerGewalt/trailtransfer/issues/1).
- **A job running when the worker stops leaves no record**, and an rclone
  process started from a shell can keep running after the worker exits
  ([#1](https://github.com/RainerGewalt/trailtransfer/issues/1)).
- **Redaction is a pattern, not a guarantee.** It masks the value after
  `password`, `pass`, `secret`, `token`, `api_key`, `key` and `credential`
  followed by `=` or `:`. It does not catch credentials in URLs
  (`user:password@host`), `Authorization: Bearer` headers,
  `X-Amz-Signature=` or names such as `secret_access_key = …`. Keep
  credentials out of paths and remote names.
- **Symbolic links behind a remote.** Links under a remote alias that points
  to a local folder cannot be resolved by the policy, because TrailTransfer
  does not read `rclone.conf`; keep such folders free of links that point
  elsewhere. Links inside a source tree are not followed: rclone skips them
  without `-L`, which TrailTransfer never passes. The check and the transfer
  are two steps; someone who can change the file system between them can
  redirect a local path. An MQTT sender cannot.
- **Case and Unicode are compared exactly.** On a backend that ignores case, a
  differently cased spelling of an allowed path is refused rather than
  matched.
- **The size limit filters silently.** Files above `max_file_size_mb` are
  excluded by rclone; the worker cannot count them. The record says that the
  limit applied, not which files it excluded.
- **Status answers and the deduplication set grow** with the number of
  distinct `job_id`s a worker process handles.

## Deployment hardening

- Require broker authentication and an ACL that lets only authorised senders
  publish to `…/commands` and only the worker publish to its own topics.
- Keep broker traffic on the host or a trusted segment, or tunnel it (see
  "No TLS to the broker").
- Pin the rclone version you deploy. It is not reported in capabilities or
  results; the container image ships rclone 1.68.
- Mount `/config` and the source data read-only and give the worker only the
  directories its policy names.
- The Docker Compose demo is for a local evaluation only: it publishes an
  anonymous Mosquitto on port 1883 and an S3 server with demo credentials on
  port 9000 on all interfaces of the host.
- Broadcast commands (`trailtransfer/all/commands`) are not implemented.

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

- Reports are handled confidentially, and nothing from a report is copied
  into a public issue. A confirmed vulnerability stays confidential. Once it
  has been fixed, a coordinated disclosure, for example as a GitHub security
  advisory, may follow, crediting you if you want that. Neither a fix nor a
  publication date is promised.
- Reports are read and answered on a best-effort basis by a single
  maintainer. There is no guaranteed response or fix time, and none is
  implied by this document.
- A report that includes a reproduction is acted on before one that does not.
- If a fix is made, it ships in a regular release. There is no backport to
  older releases.
