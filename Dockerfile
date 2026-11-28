# syntax=docker/dockerfile:1

# Base images are pinned by tag and index digest: the tag says what it is,
# the digest decides what is pulled. Dependabot proposes digest updates
# (.github/dependabot.yml); each one is a reviewed change.

# ---- build ----
FROM golang:1.26@sha256:4165ee6bf3b7811fd7b40373290405c83d0167dcb87ee5f29c427cdcef45ff8f AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/RainerGewalt/trailtransfer/internal/version.Version=${VERSION}" \
    -o /out/trailtransfer ./cmd/trailtransfer

# ---- rclone (pinned) — the transfer engine, called as a subprocess ----
FROM rclone/rclone:1.76@sha256:7cbeec24a44f206067597c0368be4de90ae19a64d5113c7d1938bd737a81e466 AS rclone

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.title="TrailTransfer" \
      org.opencontainers.image.description="MQTT-controlled rclone worker: a policy decides which transfers run, and each decision and outcome is recorded in a hash-chained journal." \
      org.opencontainers.image.source="https://github.com/RainerGewalt/trailtransfer" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build  /out/trailtransfer      /usr/local/bin/trailtransfer
COPY --from=rclone /usr/local/bin/rclone   /usr/local/bin/rclone
USER nonroot:nonroot
# /config: worker-policy.yaml + rclone.conf (read-only). /data: source files.
VOLUME ["/config", "/data"]
ENTRYPOINT ["/usr/local/bin/trailtransfer"]
CMD ["run"]
