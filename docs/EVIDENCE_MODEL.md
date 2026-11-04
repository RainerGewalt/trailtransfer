# Evidence Model

TrailTransfer turns each terminal job outcome into a **tamper-evident,
independently verifiable record**. This is what makes transfers *audit-ready*
rather than just logged. It needs no database, no cloud, and no TrailMQ.

The record format is the **Trail Evidence Record v1** (`trail.evidence/1`),
the shared evidence format of the Trail projects. Its reference specification
and test vectors live in the sister project
[trail-evidence](https://github.com/RainerGewalt/trail-evidence), whose
verifiers (Go, Python, browser) also check TrailTransfer journals; this
document describes the format as TrailTransfer implements it, and `internal/evidence/testdata/vectors.json` holds the vectors this
implementation produces. The JSON Schemas are in
[`../schemas/trail-evidence-record.schema.json`](../schemas/trail-evidence-record.schema.json)
(generic record) and
[`../schemas/transfer-job-result.schema.json`](../schemas/transfer-job-result.schema.json)
(TrailTransfer's result payload on top of it).

## The chain, in one picture

```
command (untrusted) ─▶ policy decision ─▶ rclone run ─▶ result payload
                                                              │  seal
                                                              ▼
   record N-1 ──chain_hash──▶ prev_hash │ record N │ chain_hash ──▶ prev_hash │ record N+1
                             (seq n-1)              (seq n)                    (seq n+1)
                         append-only JSONL journal (fsync)  ──▶  trailtransfer verify
```

Each result is wrapped in a **record** and linked to the previous one, so
removing, altering or reordering any record breaks the chain and is detected
by `verify`.

## The record

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
  "payload": { "…the result event: action, source, target, status, counters,
                command_hash, policy_version, policy_hash, worker_version…" },
  "content_hash": "sha256:…",
  "chain_hash": "sha256:…",
  "job_id": "job-001",
  "chain_restart": true,
  "anchor_hash": "sha256:…",
  "signature": { "alg": "ed25519", "key_id": "ed25519:…", "sig": "…" }
}
```

| Member | TrailTransfer fills it with |
|---|---|
| `schema` | always `trail.evidence/1` |
| `producer` | `kind` `transfer-worker`, `id` = `worker_id`, `version` = build version |
| `stream_id` | `worker_id`; one worker is one stream |
| `seq` | 0 for the first record of a stream, then +1 per record |
| `prev_hash` | `chain_hash` of the previous record; all zeros (genesis) for the first |
| `event_type` | `transfer.job.result` for completed, failed and cancelled jobs; `transfer.job.rejected` for jobs turned away by schema or policy |
| `time` | `recorded_at` = sealing time in RFC 3339 UTC; `source` = `system` |
| `actor` | `auth` `self-declared` with `id` = the command's `operator`, or `auth` `none` |
| `policy` | `id` = `policy_id` from the config (default: the policy file name), `version` = `policy_version`, `hash` = SHA-256 of the policy file bytes, `kind` `transfer-policy` |
| `payload` | the result event, unchanged, including `command_hash` and `worker_version` |
| `content_hash` | `SHA-256(JCS(payload))` |
| `chain_hash` | `SHA-256(JCS(core))`, see below |
| `job_id` | the job's id, for lookup; not part of the core |
| `chain_restart`, `anchor_hash` | present on the first record of each process, see "Restarts" |
| `signature` | optional Ed25519 signature, see "Signing" |

- **Canonicalisation:** [RFC 8785 JSON Canonicalization Scheme (JCS)](https://www.rfc-editor.org/rfc/rfc8785)
  gives deterministic bytes regardless of key order, so hashes are stable and
  reproducible by anyone. TrailTransfer uses `github.com/gowebpki/jcs`, the
  same library as the trail-evidence reference implementation, so both
  canonicalise byte for byte identically.
- **Numbers:** RFC 8785 renders numbers with ES6 semantics. Integers outside
  ±2^53 lose precision (2^53+1 canonicalises to 9007199254740992) and floating
  point values are re-rendered. Any payload value whose exact decimal
  rendering matters, such as integers beyond ±2^53 or rounding-relevant
  floats, must be transported as a **string**. TrailTransfer's own result
  payload contains only small integers (counters, byte totals, exit codes);
  `bytes_transferred` would only reach 2^53 at about 9 PB per job.
- **content_hash** proves the payload was not altered.
- **chain_hash** covers exactly the core members `schema`, `producer`,
  `stream_id`, `seq`, `prev_hash`, `event_type`, `time`, `actor`, `policy`,
  `content_hash`, plus `chain_restart` and `anchor_hash` when present. The
  payload enters through `content_hash`; `job_id`, `signature` and `context`
  are outside the core.
- **Hashes** are written as `sha256:` followed by 64 lowercase hex characters.

## Restarts

Every process start is a **chain restart**. The first record a worker seals
after starting carries `chain_restart: true` and `anchor_hash`:

- with an existing journal, `anchor_hash` is the `chain_hash` of the last
  record in the file, `seq` continues after it, and `prev_hash` equals
  `anchor_hash`;
- with an empty or missing journal, and in the in-memory mode without a
  journal, `anchor_hash` is the genesis hash and `seq` starts at 0.

`verify` reports restarts separately from breaks. A restart whose
`anchor_hash` does not continue the previous record in the file, or whose
`prev_hash` differs from `anchor_hash`, is a chain break: a segment is hidden
or forged.

A journal whose last line cannot be read is not extended; the worker refuses
to start on top of it so that the damage stays visible.

## The journal

Set `TRAILTRANSFER_EVIDENCE_JOURNAL=/data/evidence/journal.jsonl` on a
writable, ideally WORM, volume. Each sealed record is appended as one JSON
line (UTF-8, `\n`) and `fsync`'d. Without a journal the chain links records
within one process lifetime only; enable the journal for durable evidence.

## Verification

```bash
trailtransfer verify /data/evidence/journal.jsonl
# OK: 42 record(s), 3 restart(s)                       (exit 0)
# FAIL: 42 record(s), 3 restart(s)                     (exit 1)
#   hash_mismatch      1
#   chain_break        1
#   seq_gap            1
#   - record #17 (seq 17): hash_mismatch: content_hash does not match payload
#   …
```

`verify` walks the file in order and, for every record, checks

1. `schema_invalid`: the record is `trail.evidence/1` with all required
   members, valid hash formats, enumerations and an RFC 3339 UTC time;
2. `hash_mismatch`: `content_hash` recomputed from `payload`, and
   `chain_hash` recomputed from the core, equal the stored values;
3. `chain_break`: `prev_hash` equals the previous record's `chain_hash` (or
   `anchor_hash` on a restart, which in turn must continue the previous
   record), and `stream_id` does not change;
4. `seq_gap`: `seq` equals the previous `seq` + 1 unless the record is a
   restart;
5. `restart`: informational, counted separately and never a failure;
6. `signature_invalid`: with `--pubkey`, the Ed25519 signature verifies.

Every record is checked; one damaged record does not hide the rest, and the
walk re-synchronises after each record so a single break yields a single
finding. An auditor runs it against the file alone.

## Signing (Ed25519, optional)

Generate a key pair and run the worker with the private key; every record is
then signed over the **ASCII bytes of the `chain_hash` string**. The signature
is base64url without padding. Signing gives non-repudiation and defeats a
whole-suffix rewrite by someone who can write the journal but lacks the key.

```bash
trailtransfer keygen --out worker-01           # writes worker-01.key / worker-01.pub
TRAILTRANSFER_SIGNING_KEY=worker-01.key trailtransfer run --config config.yaml
trailtransfer verify --pubkey worker-01.pub /data/evidence/journal.jsonl
# OK: 42 record(s), 3 restart(s), 42 signature(s) verified
```

Keep the private key on the worker (or in an HSM/KMS in production) and
distribute only the public key to reviewers. `key_id` is a short digest of
the public key; key rotation and revocation are operator processes that this
release does not automate.

## Attribution and time: the honest limits

- **actor** is *self-declared* via the command's `operator` field. MQTT 3.1.1
  does not forward the publisher's authenticated identity to subscribers, so
  strong attribution is a broker or governance responsibility: broker ACLs,
  mTLS, MQTT 5 user properties, or TrailMQ. The record says so in
  `actor.auth`.
- **time.source** is `system`. For regulated use keep the host on NTP
  (ISO/IEC 27001:2022 control 8.17). Trusted timestamps (RFC 3161) and the
  `tsa` source are not implemented.
- The chain proves integrity and order of the records TrailTransfer wrote. It
  does not prove the content of the transferred files; a per-file manifest is
  future work.

## Threat coverage and what closes the gaps

| Threat | Detected? | Finding |
|---|---|---|
| A record altered | yes | `hash_mismatch` |
| A record deleted, inserted or reordered | yes | `chain_break` and `seq_gap` |
| A segment hidden behind a forged restart | yes | `chain_break` on the restart record |
| Whole-suffix rewrite by a journal writer | with signing | `signature_invalid`; also anchor the latest `chain_hash` externally (WORM, TSA, TrailMQ) |
| Non-repudiation of origin | with signing | Ed25519 signature with `key_id` |
| Content of the transferred files | partial | `command_hash` fixes the instruction; a file manifest is planned |

## Pre-release format

Journals written before v0.1.0 used a TrailTransfer-specific envelope
(`schema_version`, `worker_id`, `issuer`, `recorded_at` at the top level,
signatures over the digest bytes in standard base64). `verify` reports those
lines as `schema_invalid` with a pointer to
[`MIGRATION.md`](MIGRATION.md); there was no release, so no converter ships.
