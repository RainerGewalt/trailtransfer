# Release checklist for v0.1.0

Status: **prepared, not tagged**. The code and documentation for v0.1.0 live
on `feat/evidence-record-v1` (on top of `chore/repository-hygiene`). Nothing
has been pushed or merged. This list collects what the repository owner must
check or decide by hand before `git tag -a v0.1.0`; the generic procedure is in
[`RELEASE.md`](RELEASE.md).

## 1. Branches and merge path

- [ ] Decide the default branch. The workflows, badges, CODEOWNERS and
      `docs/GIT_WORKFLOW.md` assume `master` plus `staging`; the repository
      currently has `main` as its only long-lived branch. Either create
      `master`/`staging` (and retarget `main`), or change `master` to `main`
      in `.github/workflows/ci.yml`, `.github/workflows/pull-request-policy.yml`,
      the README CI badge (`?branch=master`), `CHANGELOG.md` compare links,
      `docs/GIT_WORKFLOW.md` and `docs/RELEASE.md`.
- [ ] Push `feature/trailtransfer-go-transform`, `chore/repository-hygiene`
      and `feat/evidence-record-v1` (or squash them into one release branch)
      and merge via pull request according to the branch-direction policy.
      `main` still holds the old FileFlux; the release merge replaces it.
- [ ] Enable branch protection for the release and integration branches:
      required status checks `Go quality gate` and `Branch direction`, no
      force pushes, no deletions, rules applied to administrators.

## 2. Workflows and secrets

- [ ] `.github/workflows/docker.yml` pushes to `ghcr.io/RainerGewalt/trailtransfer`.
      Confirm the workflow has `packages: write` permission and uses
      `GITHUB_TOKEN`; no long-lived registry secret should be needed. If the
      package already exists under another name, link it to the repository or
      grant the Actions token write access in the package settings.
- [ ] Run the CI workflow once on the release branch and confirm
      `docker compose config -q` passes in CI (locally it warns about an
      unset `INSTANCE_ID` that comes from a local `.env`, not from the
      compose file).
- [ ] Decide whether `govulncheck` (`make vuln`) must pass before the tag; it
      is not a CI gate yet.

## 3. Repository identity

- [ ] Repository rename `FileFlux` → `trailtransfer` is the owner's decision.
      The Go module path is already `github.com/RainerGewalt/trailtransfer`,
      so `go install` and the pkg.go.dev badge only work after the rename (or
      with a redirect). If the repository stays `FileFlux`, change the module
      path or document that the module cannot be fetched by its path.
- [ ] After a rename, update the GitHub URLs in `README.md` badges,
      `.github/ISSUE_TEMPLATE/config.yml`, `CHANGELOG.md` and the `$id` fields
      in `schemas/*.json`.

## 4. Content checks

- [ ] `go build ./... && go vet ./... && go test -race ./...` green on the
      merge commit (last local run: all packages pass, 39 evidence tests).
- [ ] `make demo`, send `examples/commands/check.json` and one rejected
      command, then `make verify-demo`: expect `OK: N record(s), 1 restart(s)`.
      Restart the worker container and send another command: expect
      `2 restart(s)` and no `chain_break`.
- [ ] Sign a demo journal (`trailtransfer keygen`, `TRAILTRANSFER_SIGNING_KEY`)
      and verify with `--pubkey`.
- [ ] Move the `## [Unreleased → 0.1.0]` section in `CHANGELOG.md` to
      `## [0.1.0] - <tag date>` and add the compare link; keep an empty
      `[Unreleased]` above it. Adjust the date if the tag is not created on
      2026-11-02.
- [ ] Read `README.md`, `docs/EVIDENCE_MODEL.md`, `docs/MQTT_CONTRACT.md`,
      `docs/COMPLIANCE.md` once more for claims that exceed the code: no
      GxP, Part 11, immutability, WORM or certification statements.
- [ ] Decide whether the sister project trail-evidence will be referenced by
      link in the README; today it is named without a link because it is not
      published.

## 5. Tag and publish

- [ ] Annotated tag on the merge commit: `git tag -a v0.1.0 -m "v0.1.0"`,
      then `git push origin v0.1.0`.
- [ ] Confirm the GHCR image `v0.1.0` and `latest` exist and run
      `docker run --rm ghcr.io/RainerGewalt/trailtransfer:v0.1.0 version`.
- [ ] Create the GitHub Release from the tag with the 0.1.0 changelog section.
