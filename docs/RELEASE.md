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

1. Ensure `main` is green and up to date (`make lint`, `make test`,
   `go build ./...`); see [`GIT_WORKFLOW.md`](GIT_WORKFLOW.md).
2. In a release pull request into `main`:
3. Move the `## [Unreleased]` section of [`CHANGELOG.md`](../CHANGELOG.md) to a
   new `## [X.Y.Z] - YYYY-MM-DD` section and add fresh links.
4. Confirm the docs are consistent (no contradictions with README /
   `docs/EVIDENCE_MODEL.md` / `docs/MIGRATION.md`) and the JSON Schemas are
   stable for this version.
5. Merge the release pull request once CI is green, update your local `main`,
   then create an annotated tag on that commit:

   ```bash
   git tag -a v0.3.0 -m "v0.3.0"
   git push origin v0.3.0
   ```

6. Pushing the tag triggers `.github/workflows/docker.yml`, which builds a
   multi-arch (`linux/amd64`, `linux/arm64`) image and pushes it to
   `ghcr.io/rainergewalt/trailtransfer`, tagged with the version without `v`
   (`0.3.0`) and `latest`; the binary in it reports that same version.

## Release checklist

- [ ] CI green on `main`
- [ ] Release pull request into `main` merged with green CI
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
