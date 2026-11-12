# Contributing to TrailTransfer

Thanks for your interest in TrailTransfer — an MQTT-controlled, policy-guarded,
evidence-producing rclone worker for edge file transfers.

Before opening a large PR, please read this file and
[`docs/PRODUCT_MODEL.md`](docs/PRODUCT_MODEL.md). TrailTransfer has a deliberately
**narrow scope**; changes that widen it are usually declined.

## Scope: what belongs in the core

A change belongs in TrailTransfer only if it improves the **safe acceptance,
execution, recovery, observation or verification** of a transfer job. Concretely,
ask:

1. Does it improve **controllability** (policy, safe command handling)?
2. Does it improve **security** (containment, least privilege, no new attack
   surface)?
3. Does it improve **provability** (evidence, verification, traceability)?

If the answer to all three is "no", it probably does not belong here.

**Explicit non-goals** (please do not propose these): a web UI, a generic
scheduler or workflow engine, a remote-shell / arbitrary-command interface,
arbitrary rclone flags from MQTT, a custom SMB/SFTP/S3/WebDAV implementation, an
embedded broker, a required cloud control plane, required TrailMQ integration,
hidden telemetry, a broad REST API, an rclone replacement, or a generic
file-browser / delete surface. See [`docs/PRODUCT_MODEL.md`](docs/PRODUCT_MODEL.md).

## Hard rules (non-negotiable)

These are security invariants. A PR that violates any of them will not be merged:

- **No shell execution.** rclone is invoked only via `exec.CommandContext` with an
  argument vector — never `sh -c`, never string interpolation.
- **No free-form rclone flags/args from MQTT.** The action→argv mapping is fixed.
- **MQTT commands are untrusted input.** Strict schema validation (unknown fields
  rejected) plus local policy enforcement are mandatory.
- **No secrets in logs, events, tests or docs.** Backend credentials live only in
  the operator-provided, read-only `rclone.conf` and are referenced by alias.
- **No compliance, certification or validation claims.** Documentation
  describes what the code does and where it stops, with a test or a
  reproducible scenario behind each security statement (see
  [`SECURITY.md`](SECURITY.md)); it does not map TrailTransfer to regulatory
  frameworks.
- **TrailMQ stays optional.** TrailTransfer must remain fully usable against any
  standards-compliant MQTT broker.
- **Evidence stays verifiable.** Preserve the JCS hashing, `content_hash`,
  `chain_hash`, `seq`/`prev_hash`, append-only journal and `trailtransfer verify`.
  Rejections are sealed evidence records too.

## Local development

Requires Go (see `go.mod` for the version), `rclone` on `PATH` for end-to-end runs,
and Docker for the demo.

```bash
make lint      # gofmt check + go vet
make test      # go test -race ./...
make cover     # per-package coverage
make build     # build ./cmd/trailtransfer
make demo      # docker compose up --build (Mosquitto + S3 target + worker)
make verify-demo   # independently verify the demo's evidence journal
```

Please make sure `make lint` and `make test` pass before pushing.

## Pull requests

- Keep PRs **small and focused**. Do not mix cosmetic changes with
  security-sensitive behavior changes.
- Add or update tests for behavior changes. Security-relevant code (command
  decoding, policy, path/remote containment, argv construction, evidence) should
  come with table-driven and, where useful, fuzz tests.
- Update the relevant document under `docs/` when you change normative behavior.
- Add a `CHANGELOG.md` entry under **Unreleased**.
- Fill in the PR template checklist.

## Commit style

We use [Conventional Commits](https://www.conventionalcommits.org/), e.g.
`feat(policy): …`, `fix(rclone): …`, `docs(evidence): …`, `test(jobs): …`.

A `Signed-off-by` line ([DCO](https://developercertificate.org/)) is encouraged:
`git commit -s`.

## Reporting security issues

Do **not** open a public issue for vulnerabilities. Follow
[`SECURITY.md`](SECURITY.md) (private GitHub security advisory).

## Code of conduct

Participation is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
