# Compliance & Data Integrity

> **Honest scope.** TrailTransfer is open-source software. Software cannot *be*
> "GMP compliant" or "Part 11 certified" on its own — compliance is a property of
> a *validated system in a controlled process*. TrailTransfer is **designed to
> support** data integrity (ALCOA++) and to be **validatable** by the operator.
> It makes no certification claim. This page maps features to expectations and
> states clearly what the operator must provide.

## ALCOA++ mapping

| Principle | How TrailTransfer supports it |
|---|---|
| **Attributable** | `producer`, `command_hash`, self-declared `actor` (command `operator`), and optional Ed25519 signature (`key_id`); strong identity via broker ACL/mTLS (operator) |
| **Legible** | structured JSON, `schema` `trail.evidence/1`, published JSON Schemas (`schemas/`) |
| **Contemporaneous** | `time.recorded_at` (RFC 3339 UTC) at seal time; `time.source` declared; NTP is operator responsibility (ISO 27001 8.17) |
| **Original** | the sealed journal record is the original; hashes fix its content |
| **Accurate** | `rclone_exit_code`, transfer counters, captured errors; per-file manifest planned ([#2](https://github.com/RainerGewalt/trailtransfer/issues/2), milestone 0.2.0) |
| **Complete** | completed **and** rejected jobs are recorded; journal + fsync. Not yet: an outcome is lost if the worker crashes before it is sealed (durable job state and outbox planned in [#1](https://github.com/RainerGewalt/trailtransfer/issues/1)), and records cut off at the end of the journal are not detectable without an external anchor |
| **Consistent** | monotonic `seq` + `prev_hash` hash chain (chronological, gap-detecting); restarts anchored and reported separately |
| **Enduring** | append-only journal on a durable/WORM volume; new segments anchored to the last record on restart |
| **Available** | `trailtransfer verify` + the plain-text journal; readable without any service |
| **Traceable** | chain of custody: actor → policy (`policy.id`, `policy.hash`) → execution → sealed result |

## EU GMP Annex 11 / 21 CFR Part 11 (indicative)

| Expectation | TrailTransfer | Operator provides |
|---|---|---|
| Audit trail: secure, time-stamped, computer-generated | sealed result records, hash-chained | retention store, review process |
| Audit trail not alterable | changes are detectable through the hash chain and `verify`, not prevented; see the threat table in `EVIDENCE_MODEL.md` | WORM storage, access control |
| Access / authority checks (Annex 11 §12) | policy allowlists; command schema | broker auth + ACL, mTLS, RBAC |
| Electronic records retained & retrievable | JSONL journal (copy the file to archive it), `verify` | retention period, backup |
| Electronic signatures (Part 11 Subpart C) | not provided: the optional Ed25519 signatures (`keygen`, `verify --pubkey`) are machine signatures of the worker's key, not a person's electronic signature | signature process, key management/HSM |
| Data integrity & time | JCS hashes, RFC 3339, `time.source` | NTP; optional RFC 3161 TSA |

*Indicative only — not a gap assessment. Map against your own SOPs.*

## GAMP 5 / CSV

TrailTransfer is small, deterministic and testable (see `go test ./...` and
`docs/EVIDENCE_MODEL.md`). For validation: treat it as configurable software,
trace your **user requirements** to the policy + MQTT contract + evidence model,
and decide whether `verify` and the example commands can serve as test evidence in your own qualification. `policy_version` /
`policy_hash` and `worker_version` in every record support change control.

## Standards referenced

ALCOA++ (WHO/PIC-S/MHRA/FDA data integrity) · EU GMP Annex 11 · 21 CFR Part 11 ·
GAMP 5 · ISO 8601 · ISO/IEC 27001:2022 (8.15 logging, 8.16 monitoring, 8.17 clock
sync) · ISO/IEC 27037 (chain of custody) · ISO/IEC 27040 (WORM/storage security)
· NIST SP 800-92 (log mgmt), 800-57 (key mgmt) · RFC 8785 (JCS), RFC 6962
(chained/Merkle logs), RFC 3161 (trusted timestamps), RFC 8032 (Ed25519) · JSON
Schema 2020-12.

## Shared responsibility (summary)

**TrailTransfer provides:** canonical hashing, hash-chained records whose
changes `verify` detects within the limits of `EVIDENCE_MODEL.md`, an
append-only journal, policy enforcement + policy hash, structured/versioned
events, JSON Schemas.

**The operator provides:** NTP time sync; broker authentication, ACLs and TLS/mTLS;
a WORM/immutable retention store and retention/disposal policy; backups; key
management (for signatures); and the **validation** of the deployed system against
their own regulated process.
