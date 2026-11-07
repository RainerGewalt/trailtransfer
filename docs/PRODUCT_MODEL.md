# Product Model

**TrailTransfer runs controlled file transfers at the edge: policy-restricted
rclone jobs over MQTT, with every outcome recorded in a verifiable journal.** It
turns rclone into a policy-guarded, observable, evidence-oriented transfer
worker.

## Core flow

```
MQTT command in
  → policy validation
  → controlled rclone execution (argv, never a shell)
  → progress / status / result out
  → evidence event for audit / review
```

## What it is

- A small **edge worker**, controlled over **MQTT**.
- Constrained by a **local policy** (allowlists + guards).
- A **controlled runner of rclone** — rclone stays the transfer engine.
- A publisher of **status / progress / result / logs / health / capabilities**.
- **Standalone** against any MQTT broker, TrailMQ included.

## What it is not

- Not an rclone replacement, and not its own SMB/SFTP implementation.
- Not an enterprise MFT suite.
- Not a remote shell or a generic file manager.
- Not a cloud service, and not a REST-first file service.
- Not dependent on TrailMQ.

## Why Go + rclone

Go gives static single-binary edge deployment, clean subprocess handling and
`context` cancellation, and a large DevOps/IIoT contributor base. rclone is a
mature, MIT-licensed engine with 40+ backends — TrailTransfer orchestrates it as
a subprocess instead of reimplementing transfers.

## Ecosystem

TrailSource reads OPC UA sources and TrailTransfer moves files; both write
Trail Evidence Records that the
[trail-evidence](https://github.com/RainerGewalt/trail-evidence) verifiers
check. TrailMQ is a policy-controlled MQTT broker both can connect to as
ordinary MQTT clients; it does not read their journals.

See [`CONCEPT.md`](CONCEPT.md) for the full product/OSS strategy and
[`ARCHITECTURE.md`](ARCHITECTURE.md) for the technical blueprint.
