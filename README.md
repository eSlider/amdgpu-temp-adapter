# amdgpu-temp-adapter

A small Linux tool that keeps an AMD Ryzen/APU cool during long CPU/GPU batches.
It caps the **package** (temperature + power) instead of the clock, so a batch
stays cool *and* fast, and a shared iGPU is not throttled into the CPU.

It is a thin wrapper: it shells out to an existing
[`ryzenadj`](https://github.com/FlyGoat/RyzenAdj) binary for the SMU limits and
adds a **root-less closed-loop watchdog** that pauses/resumes a process group
around temperature thresholds. It is **not** upstream RyzenAdj and does not
talk to the SMU itself.

> This repository was previously named `eSlider/ryzenadj`; it was renamed to
> avoid clashing with the upstream project. The older closed-loop governor code
> is preserved on the `legacy-governor` branch.

## Commands

```sh
amdgpu-temp-adapter apply    [-tctl C] [-stapm mW] [-slow mW] [-fast mW] [-ryzenadj PATH] [-dry-run]
amdgpu-temp-adapter watchdog -pgid N [-high C] [-low C] [-poll D] [-min-pause D] [-log PATH] [-sensors CSV]
amdgpu-temp-adapter temp     [-sensors CSV] [-json]
```

- **apply** — push a Tctl ceiling and STAPM/SLOW/FAST power caps into the SMU
  via `ryzenadj`. The SMU values are volatile: re-run after every boot (the
  shipped `amdgpu-temp-adapter.service` does that). Needs root.
- **watchdog** — root-less backstop: `SIGSTOP`s a process group when the package
  crosses `-high` and `SIGCONT`s it below `-low`, with a minimum pause. Always
  `CONT`s on startup and on TERM/INT so a batch is never left frozen. Start the
  batch with `setsid` so it is its own process-group leader, then pass `-pgid`.
- **temp** — print the hottest package sensor in °C (diagnostics/scripting).

## Build

```sh
make build          # -> ./amdgpu-temp-adapter
make test           # go test -race ./...
make vet
```

Requires Go and an existing `ryzenadj` binary (default `/usr/local/bin/ryzenadj`).

## Usage

```sh
# apply a moderate policy (Tctl 85 C, PPT 88/76/105 W)
sudo ./amdgpu-temp-adapter apply

# run a long batch under the watchdog
setsid ./heavy-batch &          # becomes its own process-group leader
./amdgpu-temp-adapter watchdog -pgid $!
```

Defaults target a Ryzen 7 7700X (Raphael, AM5, 8C/16T, TDP 105 W, stock PPT
142 W); every value is a flag, so other SKUs just need different numbers.
Sensors default to `k10temp,amdgpu` plus all thermal zones.

## Install

```sh
sudo install -m 0755 amdgpu-temp-adapter /usr/local/bin/amdgpu-temp-adapter
sudo install -m 0644 amdgpu-temp-adapter.service /etc/systemd/system/
sudo systemctl enable --now amdgpu-temp-adapter.service
```

## Related

- [FlyGoat/RyzenAdj](https://github.com/FlyGoat/RyzenAdj) — the upstream SMU tool
  this wraps.
- [`legacy-governor` branch](https://github.com/eSlider/amdgpu-temp-adapter/tree/legacy-governor)
  — the older closed-loop governor (this repo before the rename; the old
  `eSlider/ryzenadj` URL redirects here).
- [eSlider/btop](https://github.com/eSlider/btop) — btop fork with Intel `xe` +
  AMD iGPU monitoring used to watch these batches.

## License

MIT — see [LICENSE](LICENSE).
