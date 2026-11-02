# TrailTransfer Product and Open-Source Strategy

> **Product claim:** MQTT-controlled rclone worker for audit-ready edge file
> transfers.

This document explains the product intent, boundaries, and open-source strategy
for TrailTransfer. It is not a protocol specification. The normative technical
contracts are documented in [`MQTT_CONTRACT.md`](MQTT_CONTRACT.md),
[`POLICY.md`](POLICY.md), [`EVIDENCE_MODEL.md`](EVIDENCE_MODEL.md), and
[`SECURITY_POLICY.md`](SECURITY_POLICY.md).

The maintained runtime is written in Go under `cmd/` and `internal/`. The
original FileFlux Rust implementation is archived under `legacy/rust/` as a
non-production migration reference; it is not built, tested, or shipped.

## Executive summary

TrailTransfer is a small edge worker that accepts file-transfer jobs over MQTT,
checks them against an operator-controlled local policy, executes an approved
rclone action without invoking a shell, and publishes structured lifecycle and
result events.

```text
MQTT command in
  -> local policy decision
  -> controlled rclone execution (argument vector, never a shell)
  -> status, progress, and terminal result events
  -> sealed, hash-chained evidence record
```

rclone remains the transfer engine. TrailTransfer adds the control plane around
it: a closed command contract, policy enforcement, bounded job execution,
observability, and independently verifiable evidence.

The product is designed for edge, OT, IIoT, and self-hosted environments where
systems should make outbound connections but should not expose SSH, an HTTP API,
or a general remote-command surface. It works with any standards-compliant MQTT
broker. TrailMQ can add fleet governance and centralized evidence, but it is not
required.

## The problem

Edge systems routinely produce inspection images, production reports, logs,
diagnostic bundles, and CSV, JSON, or XML exports. Those files often need to
reach SMB, SFTP, S3-compatible, WebDAV, NAS, or other storage systems.

The transfer itself is rarely the difficult part. The operational problems are
control and proof:

- cron jobs and local scripts drift between machines and are difficult to
  review centrally;
- SSH provides much broader authority than a file-transfer job requires;
- inbound APIs increase the attack surface of segmented edge networks;
- failures, retries, and outcomes are hard to correlate across a fleet;
- operators cannot reliably reconstruct which instruction was accepted under
  which policy; and
- ordinary logs do not provide a tamper-evident record of terminal outcomes.

TrailTransfer addresses those problems without reimplementing storage
protocols. MQTT supplies the asynchronous control channel, the local policy
defines the worker's authority, rclone performs the transfer, and the evidence
layer records what the worker decided and observed.

## Product boundaries

TrailTransfer is:

- an MQTT-controlled edge transfer worker;
- a policy-guarded rclone runner;
- a bounded job manager with cancellation and progress reporting;
- a publisher of status, result, health, and capability events; and
- an evidence-producing component whose journal can be checked independently.

TrailTransfer is not:

- a replacement for rclone or a new SMB, SFTP, S3, or WebDAV implementation;
- a remote shell, generic command runner, or unrestricted file manager;
- a broad delete or purge API;
- a scheduler, workflow engine, or web-based file browser;
- an enterprise managed-file-transfer suite;
- a cloud service or telemetry service; or
- dependent on TrailMQ or any other proprietary control plane.

This narrow scope is deliberate. Features belong in the core when they improve
the safe acceptance, execution, recovery, observation, or verification of a
transfer job. Features that primarily create a new application surface should
be separate projects or integrations.

## Intended users and use cases

TrailTransfer is intended for:

- edge and OT engineers who need outbound-only transfer control on segmented
  networks;
- IIoT integrators managing the same transfer contract across heterogeneous
  devices;
- automation teams that want transfer jobs to participate in an event-driven
  workflow;
- operators modernizing access to existing NAS, SFTP, or object-storage
  systems without replacing them;
- self-hosted users replacing scattered scripts with observable jobs; and
- regulated or quality-sensitive environments that need verifiable technical
  records as part of a validated process.

Representative workloads include moving inspection images to object storage,
delivering batch reports to a controlled share, collecting diagnostic bundles,
and transporting machine-generated exports after an upstream process completes.

## Why MQTT, rclone, and Go

### MQTT

MQTT allows the worker to maintain one outbound connection while still
receiving commands and publishing results. Topic ACLs can isolate workers and
senders, QoS 1 supports at-least-once delivery for commands and results, and
retained health and capability events make current worker state discoverable.

MQTT is a transport, not an authorization system inside TrailTransfer. Broker
authentication, ACLs, and TLS or mTLS remain deployment responsibilities.

### rclone

rclone is a mature transfer engine with broad backend support. TrailTransfer
invokes it as a versioned subprocess instead of linking to its internals or
reimplementing protocols. This keeps the worker focused and makes backend
credentials an operator concern in `rclone.conf`.

### Go

Go provides a compact, statically buildable worker, straightforward context
cancellation, strong subprocess support, and an approachable contributor base
across the DevOps, cloud-native, and industrial integration communities. The
reason for using Go is operational and ecosystem fit; rclone remains an
independent subprocess regardless of implementation language.

## Current operating model

The current command contract supports the transfer actions `copy`, `move`,
`sync`, and `check`, plus the control actions `cancel` and `status`. Transfer
commands provide `source` and `target` values directly. The worker accepts no
free-form rclone argument or flag field, and unknown JSON fields are rejected.

The local policy is authoritative. It defines allowed actions, source roots,
targets, file-size and concurrency limits, the default dry-run posture, and
other worker controls. Commands cannot change those policy settings. The
worker's `policy_version` and a SHA-256 hash of the loaded policy are recorded
with terminal transfer results.

Destructive semantics must be understood explicitly:

- `move` asks rclone to remove source objects after successful transfer. In the
  current policy model it must be deliberately included in `allowed_actions`;
  operators should leave it disabled unless source deletion is intended.
- `sync` can remove target objects to make the target match the source. It must
  be included in `allowed_actions` and is additionally denied unless
  `allow_sync_delete` is enabled.
- `dry_run_default: true` is the recommended onboarding and evaluation posture.

The job manager limits concurrent work with `max_parallel_jobs`, supports
context-based cancellation, and rejects a repeated `job_id` within the current
worker process. That deduplication state is not yet durable across restarts.

The rclone runner constructs an explicit `[]string` argument vector and calls
`exec.CommandContext`; it never uses a shell. Filters are validated before they
become `--include` arguments. JSON statistics from rclone drive progress and
terminal counters.

## Security model

Incoming MQTT payloads are untrusted. Security depends on layered controls:

1. The broker authenticates clients and restricts topic access.
2. The closed command schema rejects unsupported fields and actions.
3. The local worker policy restricts action, source, target, size, concurrency,
   and dry-run behavior.
4. The runner maps approved actions to fixed rclone subcommands and never uses
   a shell.
5. Backend credentials remain in an operator-provided, read-only
   `rclone.conf`; commands and events refer to remote aliases instead.
6. Captured rclone errors are redacted before publication, and commands use
   configured remote aliases rather than embedded credentials.
7. Deployment controls such as a non-root container, read-only mounts, broker
   TLS, and least-privilege filesystem permissions limit impact if another
   control fails.

TrailTransfer does not turn a command's `operator` field into authenticated
identity. That value is self-declared because MQTT 3.1.1 subscribers do not
receive the publisher's authenticated broker identity. Strong attribution must
come from broker ACLs, mTLS identity, an external governance layer, or a future
authenticated command mechanism.

The current source and target allowlists are application controls, not a
replacement for operating-system isolation. Operators should expose only the
required source data and configuration to the worker. Further containment and
recovery hardening remains part of the runtime roadmap.

See [`SECURITY_POLICY.md`](SECURITY_POLICY.md) and the repository
[`SECURITY.md`](../SECURITY.md) for the maintained security model and private
vulnerability-reporting process.

## Evidence and auditability

Terminal transfer outcomes and job-addressable schema or policy rejections are
sealed as Trail Evidence Records (`trail.evidence/1`), the evidence format
shared by the Trail projects. The result payload records fields such as the
action, source, target, status, rclone exit code, counters, `command_hash`,
`policy_version`, `policy_hash`, and `worker_version`.

The record adds:

- `content_hash`, the SHA-256 digest of the RFC 8785 JCS canonical result
  payload;
- `seq` and `prev_hash`, which order records and link each record to its
  predecessor;
- `chain_hash`, which commits to the record core and the preceding link;
- `producer`, `stream_id`, `time`, `actor` and `policy`, which say who sealed
  the record, when, on whose instruction and under which ruleset;
- `chain_restart` and `anchor_hash` on the first record of each process; and
- an optional Ed25519 signature, including a stable key identifier.

When a journal path is configured, the worker appends one record per line,
calls `fsync`, and anchors the new segment to the last recorded chain hash on
restart. Reviewers can run
`trailtransfer verify` without a broker, database, cloud service, or TrailMQ.
`trailtransfer keygen` and `verify --pubkey` provide the implemented signing and
signature-verification workflow.

These controls make records tamper-evident; they do not make mutable storage
immutable. An operator that needs durable retention must provide protected or
WORM storage, access control, backups, and a review process. Signature assurance
depends on private-key protection and rotation. The current system clock must
be disciplined by the deployment, and the current evidence does not yet include
a per-file cryptographic manifest. Durable MQTT publication during broker
outages is also future work.

The complete format and its limits are documented in
[`EVIDENCE_MODEL.md`](EVIDENCE_MODEL.md).

## Compliance position

TrailTransfer is not "GMP compliant," "Part 11 certified," or a substitute for
validation. Compliance is a property of a deployed system operated under
controlled procedures.

TrailTransfer provides technical controls that can support an ALCOA++-oriented
data-integrity program: structured records, policy and command hashes,
hash-chained evidence, optional signatures, independent verification, and
versioned contracts.

The operator remains responsible for system validation, broker identity and
authorization, time synchronization, key management, immutable retention,
backup and restore, change control, SOPs, and periodic audit-trail review. See
[`COMPLIANCE.md`](COMPLIANCE.md) for the shared-responsibility mapping.

## Standalone operation and the Trail ecosystem

TrailTransfer must remain fully useful with a standard MQTT broker such as
Mosquitto, EMQX, or HiveMQ. TrailMQ integration is optional and additive.

```text
TrailSource   -> emits context-rich source events
TrailTransfer -> executes governed transfer jobs
TrailMQ       -> adds fleet governance, contracts, and centralized evidence
```

A governance layer can register expected workers, manage publication and
subscription rights, validate topic contracts, retain results, and present job
history. It does not replace the worker's local policy and must not become a
prerequisite for safe standalone operation.

## Open-source strategy

Transparency is part of the product's trust model. Operators in edge and
regulated environments need to inspect the code, build it internally, run it
without a cloud account, and retain an operational path independent of a
vendor. The command boundary, policy evaluation, evidence construction, and
verification logic therefore belong in the open-source core.

TrailTransfer uses the Apache License 2.0. It provides a permissive adoption
model and an explicit patent grant that is familiar to industrial users. rclone
is invoked as a separate MIT-licensed subprocess and retains its own license;
the container image includes its binary under that license.

The project should not place security, evidence, or backend capabilities behind
a closed feature gate. Sustainable commercial work may include support,
deployment hardening, integration, training, security review, and validation
documentation tailored to an operator's environment. Revenue should come from
expertise and service, not from weakening the usefulness of the open-source
core.

Contributions use small, reviewable changes, English Conventional Commits, and
tests for behavior changes. Security-sensitive changes require explicit threat
analysis and must preserve the no-shell, no-free-form-flags, no-secret-logging,
and independently verifiable evidence invariants. See
[`../CONTRIBUTING.md`](../CONTRIBUTING.md).

## Roadmap principles

The current baseline includes the Go worker, closed MQTT command schema, local
policy, bounded rclone execution, lifecycle events, retained health and
capabilities, a Docker Compose demonstration, JCS evidence envelopes, the
append-only journal, independent verification, and optional Ed25519 signing.

The next work should remain in coherent, reviewable slices:

1. Strengthen destructive-action controls and source/target containment.
2. Make job-ID deduplication and terminal-result state durable across restarts.
3. Add a durable MQTT outbox and broker-outage recovery.
4. Extend evidence with a per-file manifest, trusted timestamps, external
   anchoring or WORM export, and documented key rotation and revocation.
5. Harden release engineering with stable schemas, supply-chain attestations,
   vulnerability scanning, and deployment and validation guidance.

The roadmap does not include a general delete API, remote shell, arbitrary
rclone arguments, custom transfer backends, embedded broker, required cloud
control plane, web file manager, or broad workflow engine.

## Product decision test

Before adding a feature to the core, ask:

1. Does it improve control over which transfer jobs may run?
2. Does it reduce security or operational risk?
3. Does it make execution, recovery, or failure easier to observe?
4. Does it improve the ability to verify what happened?

If the answer to all four questions is no, the feature probably does not belong
in TrailTransfer.
