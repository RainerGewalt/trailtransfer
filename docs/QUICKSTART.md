# Quickstart (5 minutes)

No prior knowledge needed. You'll start a worker, send it a job over MQTT, watch
the result, and independently verify the audit trail.

## 1. Start the demo stack

```bash
docker compose up --build
```

This starts the worker, a Mosquitto broker and an S3 target (rclone's own S3
server, `rclone serve s3`), after a one-shot step that prepares
`./evidence/journal.jsonl` so the worker can append to it and you can read it.

## 2. Watch what the worker publishes

In a second terminal:

```bash
mosquitto_sub -h localhost -p 1883 -v -t 'trailtransfer/worker-01/#'
```

You'll immediately see retained `health` and `capabilities`.

## 3. Send a command

```bash
mosquitto_pub -h localhost -p 1883 \
  -t trailtransfer/worker-01/commands \
  -f examples/commands/check.json
```

You'll see `accepted → started → completed` on the job's topics, ending in a
sealed **evidence record** (`trail.evidence/1`) with `seq`, `prev_hash`,
`content_hash` and `chain_hash`.

Try a rejection too — this one is refused by policy (source `/etc` is not
allowed), and the rejection is itself a sealed, audited record:

```bash
mosquitto_pub -h localhost -p 1883 -t trailtransfer/worker-01/commands \
  -m '{"job_id":"demo-2","action":"copy","source":"/etc","target":"s3-demo:trailtransfer","operator":"you"}'
```

## 4. Verify the audit trail

The worker writes an append-only journal. Verify it independently — no broker,
no cloud:

```bash
docker compose exec trailtransfer trailtransfer verify /data/evidence/journal.jsonl
# OK: N record(s), 1 restart(s)
#   restart            1
```

Change one byte in that file and run it again — it will report the exact record
that broke. That's the whole point: the trail is tamper-evident.

## Next

- Interfaces: [`MQTT_CONTRACT.md`](MQTT_CONTRACT.md) · schemas in [`../schemas/`](../schemas)
- Policy: [`POLICY.md`](POLICY.md) · Security: [`SECURITY_POLICY.md`](SECURITY_POLICY.md)
- Audit/evidence: [`EVIDENCE_MODEL.md`](EVIDENCE_MODEL.md) · Compliance: [`COMPLIANCE.md`](COMPLIANCE.md)
