# Design

`ryzenadj-governor` has two independent mechanisms. Understanding the split is
the key to using it safely.

```
                  +---------------------------+
  sudo apply ---> | SMU / embedded controller |  hardware limit (temperature + power)
                  +---------------------------+
                              ^ smooth throttling
                              |
  run/watch ------> [ process group ]  <--- SIGSTOP/SIGCONT bang-bang watchdog
                              ^
                              | max(k10temp, amdgpu, acpitz) from sysfs
```

1. **Hardware limit (`apply`).** Write a package temperature target and power
   ceilings to the SMU through the external `ryzenadj` binary. The EC then
   throttles smoothly and only as much as needed. This is the preferred
   mechanism — it is both cool and fast (see `thermal-findings.md` §5).
2. **Watchdog (`run` / `watch`).** A root-less closed-loop governor that freezes
   a process group when the SoC gets too hot and resumes it once it has cooled.
   It is a backstop for when root or `ryzenadj` is unavailable, or an extra
   safety net on top of (1).

---

## Packages

| path | responsibility |
|---|---|
| `cmd/ryzenadj-governor` | CLI parsing, subcommand wiring, process lifecycle, exit codes |
| `internal/thermal` | read `hwmon`/`thermal_zone` sensors, compute maxima |
| `internal/ryzenadj` | validate limits, render args, shell out to the upstream binary |
| `internal/watchdog` | pure hysteresis state machine + Linux signalling loop |
| `internal/governor` | status rendering, GPU/sysfs attribute reads |

The dependency direction is one-way: `cmd -> governor -> {thermal, ryzenadj,
watchdog}`. `watchdog`'s state machine has no syscalls, which is what makes it
trivially unit-testable.

---

## Why shell out to `ryzenadj`?

Talking to the SMU directly requires PCI access and/or a kernel module, and
upstream [FlyGoat/RyzenAdj](https://github.com/FlyGoat/RyzenAdj) already
maintains that per-generation table. Shelling out:

- keeps this project **cgo-free** and pure Go, so it cross-compiles trivially;
- avoids duplicating (and drifting from) a large hardware-support matrix;
- makes the "no ryzenadj / no root" failure modes explicit and easy to detect.

The trade-off is an external dependency for the `apply` path — which is exactly
why the root-less watchdog exists.

---

## Hysteresis state machine

The controller is a bang-bang governor with hysteresis and a minimum pause:

```
state = RUNNING
on sample(t):
  if RUNNING and t >= HIGH:            -> PAUSE   (record pause start)
  if PAUSED  and t <= LOW
             and now-pause_start>=MIN:  -> RESUME
  else:                                 -> NOOP
```

- **Hysteresis** (`HIGH` well above `LOW`) prevents pause/resume thrash that
  would be both slow and risky for a GPU context.
- **`MIN_PAUSE`** guarantees a pause lasts long enough to actually shed heat.
- The machine is a pure function of `(temperature, time)`, so every transition
  is covered by unit tests.

---

## Safety invariants

These are non-negotiable and are enforced in code:

1. **Never leave a group stopped.** `SIGCONT` is sent to the target group on
   startup (clears a leftover stop from a previously-killed watchdog) and on
   every exit path, including `SIGTERM`/`SIGINT`.
2. **Only signal the target group.** `run` starts the child with `setsid`, so
   the child is its own session/process-group leader and the governor can never
   freeze the calling shell or unrelated processes.
3. **Validate before acting.** Temperature thresholds must have `HIGH > LOW`
   and positive timing; ryzenadj limits are range-checked; invalid input is a
   clear error, not a panic.
4. **Propagate the child's fate.** `run` returns the child's exit code and maps
   signal deaths to `128+signal`, and it forwards `SIGINT/SIGTERM/SIGHUP/SIGQUIT`
   to the child group (a `setsid` child does not receive terminal signals
   directly).
5. **No panics on missing hardware.** Missing sensors or a missing `ryzenadj`
   produce errors or "unavailable" output, never a crash.

---

## Signal flow for `run`

```
terminal SIGINT ──▶ parent (forwarder) ──▶ kill(-pgid, SIGINT) ──▶ child group
                                                     │
watchdog ──▶ kill(-pgid, SIGSTOP/SIGCONT) ───────────┘
```

The parent stays alive until the child exits, then stops the watchdog and sends
one final `SIGCONT` before returning the child's exit code.

---

## Release engineering

- **Versioning:** [release-please](https://github.com/googleapis/release-please)
  derives semver from conventional commits, maintains `CHANGELOG.md` and the
  manifest, and creates the git tag when the release PR is merged.
- **Artifacts:** `release.yml` builds `linux/amd64` and `linux/arm64` with
  `CGO_ENABLED=0`, packs `*.tar.gz` (binary + README + LICENSE) and
  `sha256sums.txt`, and attaches them to the GitHub Release.
- **Why an explicit build matrix instead of GoReleaser:** the release is a
  single CGO-free `go build`, so a matrix using only first-party actions is
  transparent and has no extra config to rot. GoReleaser shines for large
  multi-project matrices; here it would add a dependency without adding
  capability.
- **Anti-rot:** Dependabot updates both `gomod` and `github-actions` weekly, and
  `ci.yml` additionally runs on a weekly `schedule` so toolchain/action drift is
  caught even with no new commits.

---

## Non-goals

- Not a replacement for upstream RyzenAdj; it is a governor around it.
- Linux-only by design (sysfs + `setsid` + process-group signals).
- No daemon, no config file: every policy is an explicit flag, which keeps the
  blast radius of a mistake small.
