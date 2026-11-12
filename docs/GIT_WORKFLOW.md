# Git workflow

TrailTransfer is maintained by one person with occasional outside
contributions, so the workflow is deliberately small.

- `main` is the only long-lived branch. It is the default branch and it must
  always build and pass `make test`. Branch protection is not switched on in
  the repository settings; the rules below are kept by convention.
- Every change, including the maintainer's own, comes in on a short-lived
  branch (`feat/…`, `fix/…`, `docs/…`, `chore/…`) and is merged through a
  pull request once CI is green. Squash or fast-forward, no merge commits
  with unrelated history.
- Releases are tags on `main` of the form `vMAJOR.MINOR.PATCH`. Pushing a
  tag runs the release workflow, which builds and publishes the container
  image. Nothing is published without a tag.
- Hotfixes follow the same path: branch from `main`, pull request, tag.
- Commit messages use `type(scope): subject` in English. The changelog is
  updated in the same pull request as the change.

There is no `staging` branch and no separate integration flow. If the
project ever needs one, this file changes first.
