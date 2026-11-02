# Git workflow

TrailTransfer uses two long-lived branches and short-lived topic branches. The
goal is to keep `master` releasable, give normal changes an integration branch,
and retain visible merge topology.

## Branch responsibilities

- `master` is the stable, releasable, protected default branch. It receives
  reviewed release merges from `staging` and exceptional `hotfix/*` merges.
- `staging` is the protected integration branch. Normal development is merged
  here before it can enter `master`.
- `feature/*`, `fix/*`, `docs/*`, `chore/*`, `refactor/*`, and `test/*` are
  short-lived branches created from the latest `staging`.
- `hotfix/*` branches are exceptional production fixes created from `master`.
  After a hotfix reaches `master`, `master` is merged back into `staging`.

Direct pushes, force pushes, and branch deletion are prohibited for `master`
and `staging`. Repository rules apply to administrators as well as contributors.

## Branch names and commits

Use a short, descriptive English branch name, such as
`feature/transfer-routes`, `fix/cancel-race`, or `docs/operator-recovery`.

Commits follow Conventional Commits and describe one coherent change:

```text
feat(policy): add transfer route definitions
fix(mqtt): preserve terminal result ordering
security(paths): reject remote traversal
docs(routes): document direct-mode migration
```

Supported types include `feat`, `fix`, `docs`, `test`, `refactor`, `chore`,
`ci`, `build`, `perf`, and `security`. Avoid vague messages such as `updates`,
`changes`, `fix stuff`, or `wip`.

## Normal development flow

1. Update the local integration branch with `git switch staging` followed by
   `git pull --ff-only`.
2. Create a topic branch from `staging`.
3. Make small, reviewable commits and run the required checks.
4. Before final review, incorporate the latest `staging` consistently. A local
   rebase is suitable before review; use a merge update if the branch is already
   shared and rewriting it would disrupt reviewers.
5. Open a pull request into `staging`.
6. Resolve every review conversation and wait for required checks.
7. Merge with a merge commit, then delete the topic branch.

Do not open a normal feature, fix, documentation, refactor, test, or dependency
pull request directly into `master`. The branch-direction workflow rejects it.

## Releases

A release is a deliberate pull request from `staging` into `master`. The pull
request must summarize the release, identify migration and security effects,
link the changelog, and show current test evidence. Merge it with a merge commit.

After the merge, create an annotated semantic-version tag on the `master` merge
commit:

```bash
git switch master
git pull --ff-only
git tag -a v0.4.0 -m "v0.4.0"
git push origin v0.4.0
```

Do not tag an arbitrary feature merge or use a lightweight tag for a release.

## Hotfixes

Create `hotfix/<name>` from the current `master`, open it into `master`, and use
the same review and CI requirements as a release. Once merged, merge the updated
`master` back into `staging` with a merge commit so the fix is not lost from the
next release.

## Contributions from forks

External contributors may create any sensible topic branch in their fork and
open a pull request into the upstream `staging` branch. Keep the branch focused,
allow maintainers to edit it when practical, and update it from upstream before
final review. Fork pull requests do not need to use an upstream-owned branch
name, and they must never target `master` for normal development.

## Pull request quality

A pull request should include:

- a clear problem statement and scoped solution;
- explicit non-goals;
- migration and compatibility behavior;
- security, policy, MQTT contract, and evidence implications;
- tests for changed behavior and exact test evidence;
- documentation and changelog updates when behavior changes;
- no unrelated cleanup or generated artifacts.

At least one reviewer should be required once a second trusted maintainer has
write access. While the repository has only one eligible maintainer, pull
requests and all checks remain mandatory, but an impossible self-approval rule
is not enabled. CODEOWNERS still documents responsibility for sensitive paths.

## Required local checks

Run these before requesting final review:

```bash
gofmt -l ./cmd ./internal
go vet ./...
go test -race ./...
go test -cover ./...
go build ./...
docker compose config -q
make lint
make build
```

An empty `gofmt` result is success. Do not claim integration coverage unless a
real broker/rclone fixture suite was run.
