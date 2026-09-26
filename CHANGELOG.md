# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file is managed by
[release-please](https://github.com/googleapis/release-please); entries below
the first release are generated from conventional commits.

## [0.1.0](https://github.com/eSlider/ryzenadj/releases/tag/v0.1.0) - 2026-09-26

### Added

- `apply` subcommand: apply a package thermal/power policy
  (`--tctl-temp`, `--stapm-limit`, `--slow-limit`, `--fast-limit`) through the
  external `ryzenadj`, with before/after `ryzenadj -i` output, validation and
  idempotency.
- `watch` subcommand: root-less closed-loop watchdog with `HIGH`/`LOW`
  hysteresis, poll interval, minimum pause and an event log; signals an entire
  process group with `SIGSTOP`/`SIGCONT`.
- `run` subcommand: optionally apply limits, then start a command in its own
  session/process group, supervise it, forward signals and propagate exit codes.
- `status` subcommand: temperatures (`k10temp`/`amdgpu`/`acpitz`), GPU
  performance level, current `pp_dpm_sclk`, `gpu_busy_percent` and `ryzenadj -i`
  limits.
- `version` subcommand.
- Safety: `SIGCONT` on startup and on every exit path, so a watched group is
  never left frozen.
- Unit tests for temperature parsing/aggregation, flag validation and the
  hysteresis state machine.
- CI (build, vet, test, lint), release workflow with linux/amd64+arm64 tarballs
  and `sha256sums.txt`, release-please for semver and Dependabot for deps/actions.
- Documentation: `docs/thermal-findings.md`, `docs/design.md`,
  `docs/operations.md`.
