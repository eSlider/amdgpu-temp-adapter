# Contributing

Thanks for taking a look! This is a small, focused tool; PRs that keep it that
way are very welcome.

## Requirements

- Linux (the tool is Linux-only by design: sysfs, `setsid`, process-group
  signals).
- Go 1.26 or newer.
- Optional: the upstream [`ryzenadj`](https://github.com/FlyGoat/RyzenAdj)
  binary for the `apply` path.

## Development setup

```sh
git clone https://github.com/eSlider/ryzenadj
cd ryzenadj
make build          # ./bin/ryzenadj-governor
make test           # go test ./...
make lint           # gofmt check + go vet + golangci-lint
make release-check  # reproduce the release tarballs locally
```

## Before opening a PR

- `make lint` and `make test` must pass.
- Keep the code `gofmt`-clean and `go vet`-clean.
- Add or update unit tests for behaviour changes. The interesting logic lives in
  `internal/thermal` (parsing/aggregation), `internal/ryzenadj` (validation) and
  `internal/watchdog` (the pure hysteresis machine) — all easy to test without
  hardware.
- Do not introduce cgo or non-Linux build targets.

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/),
because [release-please](https://github.com/googleapis/release-please) derives
versions and the changelog from them:

```
feat: add a new flag
fix: never leave a paused group running
docs: clarify the power-saver trap
chore(deps): bump actions/checkout
```

`feat:` bumps the minor version pre-1.0, `fix:` bumps the patch, and `!` /
`BREAKING CHANGE:` marks a breaking change.

## Reporting issues

Please include:

- your CPU/APU model and iGPU (`lspci`/`gfx` target),
- output of `ryzenadj-governor status`,
- the exact command you ran and what you expected,
- whether you run as root and which `ryzenadj` version.

## License

By contributing you agree that your contributions are licensed under the
[MIT License](LICENSE).
