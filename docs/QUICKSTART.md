# Quickstart (5 minutes)

No prior knowledge needed. You'll start a worker, send it a job over MQTT, watch
the result, and verify its journal independently.

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

Put a few `.jpg` or `.png` files into `./upload_folder` (mounted read-only as
`/data/input`), then copy them to the demo S3 target:

```bash
mosquitto_pub -h localhost -p 1883 \
  -t trailtransfer/worker-01/commands \
  -f examples/commands/copy.json
```

You'll see `accepted → started → completed` on the job's topics, ending in a
sealed **evidence record** (`trail.evidence/1`) with `seq`, `prev_hash`,
`content_hash` and `chain_hash`. `docker compose exec s3 ls -l
/data/trailtransfer` lists what arrived.

Then check that source and target match, with the same filters:

```bash
mosquitto_pub -h localhost -p 1883 \
  -t trailtransfer/worker-01/commands \
  -f examples/commands/check.json
```

Try a rejection too — this one is refused by policy (source `/etc` is not
allowed), and the rejection is sealed in the journal like any other outcome:

```bash
mosquitto_pub -h localhost -p 1883 -t trailtransfer/worker-01/commands \
  -m '{"job_id":"demo-2","action":"copy","source":"/etc","target":"s3-demo:trailtransfer","operator":"you"}'
```

## 4. Verify the journal

The worker writes an append-only journal. Verify it independently — no broker,
no cloud:

```bash
docker compose exec trailtransfer trailtransfer verify /data/evidence/journal.jsonl
# OK: N record(s), 1 restart(s)
#   restart            1
# not checked:
#   - signatures: the records are not signed
#   - records cut off after the last record, or a journal replaced as a whole: …
#   - the transferred files themselves: the journal holds no file manifest
```

The lines under "not checked" are part of the answer: `OK` means the records
in this file are unchanged and in order, nothing more.

Change a value in a copy of that file and verify the copy: it reports the
record that no longer matches its hash. Without a signing key this shows edits
by anyone who does not also recompute the hashes; someone who can rewrite the
whole file can recompute them. With a signing key the signatures show which
key sealed a record; see [`EVIDENCE_MODEL.md`](EVIDENCE_MODEL.md) for what
`verify --pubkey` does and does not check.

## Next

- Interfaces: [`MQTT_CONTRACT.md`](MQTT_CONTRACT.md) · schemas in [`../schemas/`](../schemas)
- Policy: [`POLICY.md`](POLICY.md) · Security: [`SECURITY_POLICY.md`](SECURITY_POLICY.md)
- Journal and its limits: [`EVIDENCE_MODEL.md`](EVIDENCE_MODEL.md) · Security model and known limits: [`../SECURITY.md`](../SECURITY.md)
