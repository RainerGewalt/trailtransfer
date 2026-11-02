<!-- Thanks for contributing to TrailTransfer! Keep PRs small and focused. -->

## Summary

<!-- What does this change and why? Link any related issue: Closes #123 -->

## Type of change

- [ ] Bug fix
- [ ] New feature (within the project scope — see CONTRIBUTING.md)
- [ ] Documentation
- [ ] Refactor / internal
- [ ] CI / build / release

## Checklist

- [ ] `make lint` passes (gofmt + `go vet`)
- [ ] `make test` passes (`go test -race ./...`)
- [ ] Tests added/updated for behavior changes
- [ ] Relevant `docs/` updated for any normative behavior change
- [ ] `CHANGELOG.md` updated under **Unreleased**

## Security & scope impact

- [ ] Does **not** introduce shell execution or free-form rclone flags from MQTT
- [ ] Does **not** weaken schema validation or policy enforcement
- [ ] Does **not** log or embed secrets (credentials stay in `rclone.conf`)
- [ ] Does **not** add a non-goal surface (web UI, scheduler, remote shell,
      required cloud/TrailMQ dependency — see CONTRIBUTING.md)
- [ ] Policy / evidence / MQTT-contract impact described below (if any)

<!-- Describe any policy, evidence-format, or MQTT-contract impact here. -->
