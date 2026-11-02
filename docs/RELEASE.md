# Release process

TrailTransfer releases are driven by annotated Git tags of the form `vMAJOR.MINOR.PATCH`
(e.g. `v0.3.0`). This document describes what happens today and what is planned.

## Versioning

- [Semantic Versioning](https://semver.org/). While the project is pre-1.0,
  minor versions may include breaking changes; these are called out in the
  changelog.
- The version is injected at build time into
  `internal/version.Version` via `-ldflags` (see the `Makefile` and the Docker
  build arg `VERSION`). It is reported in `trailtransfer version`, in the
  `capabilities` topic, and in every result-event payload (`worker_version`).

## Cutting a release

1. Ensure `staging` is green and up to date (`make lint`, `make test`,
   `go build ./...`).
2. Open a release pull request from `staging` into `master`. Use a merge commit
   after all required checks pass and conversations are resolved.
3. Move the `## [Unreleased]` section of [`CHANGELOG.md`](../CHANGELOG.md) to a
   new `## [X.Y.Z] - YYYY-MM-DD` section and add fresh links.
4. Confirm the docs are consistent (no contradictions with README /
   `docs/EVIDENCE_MODEL.md` / `docs/MIGRATION.md`) and the JSON Schemas are
   stable for this version.
5. Merge the release pull request, switch to the updated `master`, then create
   an annotated tag on that merge commit:

   ```bash
   git tag -a v0.3.0 -m "v0.3.0"
   git push origin v0.3.0
   ```

6. Pushing the tag triggers `.github/workflows/docker.yml`, which builds a
   multi-arch (`linux/amd64`, `linux/arm64`) image and pushes it to
   `ghcr.io/RainerGewalt/trailtransfer` tagged with the semver and `latest`.

## Release checklist

- [ ] CI green on `staging`
- [ ] Release pull request from `staging` to `master` approved and merged
- [ ] `CHANGELOG.md` moved from Unreleased to the new version
- [ ] Schemas stable / breaking changes documented
- [ ] Migration notes updated if behavior changed
- [ ] Security review of the diff since the last tag
- [ ] Demo verified (`make demo` + `make verify-demo`)
- [ ] Tag pushed; GHCR image built

## Planned hardening (not yet automated)

These belong to the supply-chain / release-hardening phase and are **not** part
of the pipeline yet — do not claim them until the workflow implements them:

- Cross-compiled release binaries attached to the GitHub Release, with SHA-256
  checksums.
- SBOM (CycloneDX or SPDX) for binary and container.
- Build provenance / SLSA attestations.
- Container image signing (cosign) and image scanning (Trivy/Grype).
- `govulncheck` and a linter (`staticcheck` / `golangci-lint`) as required CI
  gates.
- Pinned action SHAs and a documented rclone-version compatibility matrix.
