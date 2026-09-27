# ryzenadj-governor

[![CI](https://github.com/eSlider/ryzenadj/actions/workflows/ci.yml/badge.svg)](https://github.com/eSlider/ryzenadj/actions/workflows/ci.yml)
[![Release](https://github.com/eSlider/ryzenadj/actions/workflows/release.yml/badge.svg)](https://github.com/eSlider/ryzenadj/actions/workflows/release.yml)
[![GitHub release](https://img.shields.io/github/v/release/eSlider/ryzenadj)](https://github.com/eSlider/ryzenadj/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A small Linux-only thermal governor for AMD APUs. It keeps the SoC cool during
long GPU batches (Vulkan inference, Whisper, llama.cpp, …) using two mechanisms:
a **hardware temperature/power limit** applied through
[FlyGoat/RyzenAdj](https://github.com/FlyGoat/RyzenAdj), and a **root-less
closed-loop watchdog** that pauses/resumes a process group around temperature
thresholds.


<img width="1267" height="1094" alt="image" src="https://github.com/user-attachments/assets/55c484d6-0198-4456-b619-4de7043239b1" />


> **Not the upstream project.** This is a *separate* utility (repo
> `eSlider/ryzenadj`, binary `ryzenadj-governor`) from
> [FlyGoat/RyzenAdj](https://github.com/FlyGoat/RyzenAdj). It does not talk to
> the SMU itself — it shells out to an existing `ryzenadj` binary. If you are
> looking for the SMU tool, you want upstream.

## Why

On an AMD Ryzen 7 5800H / `gfx90c` iGPU, sustained Vulkan inference drove the SoC
to **Tctl ~96 °C / amdgpu edge ~98 °C** — at throttle/shutdown territory for a
multi-hour run. CPU-side levers (`taskset`, `nice`, fewer threads) do **not**
help, because the heat source is the shared SoC package dominated by the iGPU.

Capping the **package** brought the same workload to **63–66 °C at full GPU
speed**, while the naive `power-saver` "fix" silently pins the iGPU to 200 MHz
(~10× slower). The measurements and reasoning are in
[`docs/thermal-findings.md`](docs/thermal-findings.md).

## Install

**Prebuilt release binaries** (linux/amd64, linux/arm64):

```sh
VERSION=v0.1.0
ARCH=amd64   # or arm64
curl -fsSLO "https://github.com/eSlider/ryzenadj/releases/download/${VERSION}/ryzenadj-governor_${VERSION}_linux_${ARCH}.tar.gz"
curl -fsSLO "https://github.com/eSlider/ryzenadj/releases/download/${VERSION}/sha256sums.txt"
sha256sum -c sha256sums.txt --ignore-missing
tar -xzf "ryzenadj-governor_${VERSION}_linux_${ARCH}.tar.gz"
sudo install -m 0755 "ryzenadj-governor_${VERSION}_linux_${ARCH}/ryzenadj-governor" /usr/local/bin/
```

**With Go (1.26+):**

```sh
go install github.com/eSlider/ryzenadj/cmd/ryzenadj-governor@latest
```

**From source:**

```sh
git clone https://github.com/eSlider/ryzenadj
cd ryzenadj
make build && sudo make install
```

The `apply` path additionally needs the upstream `ryzenadj` binary in `PATH` or
at `/usr/local/bin/ryzenadj`. The watchdog path needs nothing but this binary.

## Usage

```
ryzenadj-governor <command> [flags]

  apply     Apply a hardware thermal/power policy via the external ryzenadj (root)
  watch     Root-less closed-loop watchdog for an existing process group
  run       Apply limits (best effort), run a command in its own group and watch it
  status    Show temperatures, GPU clock/busy and ryzenadj limits
  version   Print build metadata
```

### `apply` — hardware limit (needs root)

```sh
# Verified defaults: --tctl-temp=85 --stapm-limit=30000 --slow-limit=25000 --fast-limit=35000
sudo ryzenadj-governor apply

# Override and/or preview:
ryzenadj-governor apply --dry-run --tctl-temp=80
sudo ryzenadj-governor apply --slow-limit=30000 --fast-limit=40000 --ryzenadj /usr/local/bin/ryzenadj
```

It prints `ryzenadj -i` **before** and **after**, validates every value, and is
idempotent. Settings are volatile — re-apply after reboot
([runbook](docs/operations.md)).

### `run` — apply limits, then supervise a command

```sh
ryzenadj-governor run -- ./whisper-cli -m model.bin audio.wav
ryzenadj-governor run --high 88 --low 78 --log /var/log/ryzenadj-governor.log -- ./batch
ryzenadj-governor run --no-apply -- ./already-cool-enough
```

`run` starts the child with `setsid` (its own session/process-group leader),
applies the limits first **if** root and `ryzenadj` are available, watches the
group, forwards `SIGINT/SIGTERM/SIGHUP/SIGQUIT`, and propagates the child's exit
code (`128+signal` on signal death).

### `watch` — supervise an existing process group

```sh
PGID=$(ps -o pgid= -p "$(pgrep -f './batch' | head -1)" | tr -d ' ')
ryzenadj-governor watch --pgid "$PGID" --high 88 --low 78 --min-pause 15
```

> The target **must** be its own process-group leader (start it with `setsid` or
> via `run`). Otherwise you would freeze unrelated processes too.

### `status` — inspect

```sh
ryzenadj-governor status
```

Shows every temperature sensor, the governor's watched maximum, per-card GPU
performance level / busy % / current sclk, and `ryzenadj -i` limits when readable.

## How it works

Two independent mechanisms, usable together:

1. **Hardware limit (`apply`).** `ryzenadj --tctl-temp=85` sets a package
   temperature target (plus STAPM/slow/fast power ceilings). The embedded
   controller then lowers clocks *smoothly and only as much as needed* — cool
   **and** fast. Preferred whenever root is available.
2. **Watchdog (`run`/`watch`).** A root-less bang-bang governor with hysteresis
   (`HIGH`/`LOW`) and a minimum pause. It `SIGSTOP`s the target process group
   when the SoC is too hot and `SIGCONT`s it once it has cooled. It is a
   backstop, not the primary fix.

Safety: the watchdog sends `SIGCONT` **on startup** and **on every exit path**
(including `SIGTERM`/`SIGINT`), so a group is never left frozen — a real defect
observed and fixed (see [`docs/thermal-findings.md`](docs/thermal-findings.md) §6.3).

### ⚠️ Do not "fix" heat with `power-saver`

```sh
powerprofilesctl set power-saver   # pins iGPU sclk to 200 MHz -> ~10x slower
```

It looks cool (43 °C) but turns a multi-hour batch into a multi-day run. Use the
`apply` policy instead, or the watchdog as a fallback. Details:
[`docs/thermal-findings.md`](docs/thermal-findings.md) §4.

## Documentation

- [`docs/thermal-findings.md`](docs/thermal-findings.md) — measured findings: the
  96–98 °C symptom, why CPU levers fail, the `power-saver` trap, the verified
  fix and expected 63–66 °C.
- [`docs/design.md`](docs/design.md) — architecture, the hysteresis state
  machine, signal flow and safety invariants.
- [`docs/operations.md`](docs/operations.md) — runbook: install, boot
  persistence, running batches, troubleshooting.
- [`CHANGELOG.md`](CHANGELOG.md) — release history (managed by release-please).
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — dev setup, tests, commit conventions.

## Development

```sh
make test          # go test ./...
make lint          # gofmt check + go vet + golangci-lint
make build         # -> ./bin/ryzenadj-governor
make release-check # reproduce linux/amd64+arm64 tarballs + checksums
```

## License

[MIT](LICENSE) © 2026 Andriy Oblivantsev
