# Thermal findings: keeping an AMD APU cool during long GPU batches

**Read this before launching a multi-hour GPU batch.** It is the difference
between a run that completes cool and fast and one that either cooks the SoC
(96–98 °C) or silently turns into a multi-day run because somebody "fixed" the
heat the wrong way.

These findings were measured on one host under sustained Vulkan load, then
folded into `ryzenadj-governor`. They are reproduced here so the defaults are
not cargo-culted.

- **Status:** the primary fix was **verified under full load**; the rest is
  measured.
- **Audience:** anyone running `whisper-cli` (Vulkan) or `llama-server`
  (Vulkan) for hours on an AMD APU.

---

## TL;DR

**Primary recipe — one line, verified:**

```sh
sudo ryzenadj --tctl-temp=85 --stapm-limit=30000 --slow-limit=25000 --fast-limit=35000
```

Under full `whisper-medium` (Vulkan, 16 host threads, `balanced` profile, no
CPU pinning) this holds the SoC at **Tctl 63–66 °C / amdgpu edge 58–65 °C**
(was 96/98 °C) with the GPU at **1487–2000 MHz, 89–98 % busy — full speed, not
throttled**, drawing ~25 W. Settings are volatile: **re-run after every
reboot** (or use the optional systemd unit in `operations.md`). If root is ever
unavailable, the closed-loop **watchdog** is the fallback.

Everything below explains why.

---

## 1. Host and workload shape

| item | value |
|---|---|
| CPU/SoC | AMD Ryzen 7 5800H (Cezanne), 8C/16T |
| iGPU | Radeon Vega integrated, **`gfx90c`**, **UMA** (shares system RAM) |
| GPU runtime | Vulkan / RADV (RENOIR) |
| Workload | `whisper-cli` + a medium Whisper model, and separately `llama-server`, both Vulkan |
| VRAM carve-out | ≈ 2 GB (GPU memory pressure shows up as system-RAM / swap pressure) |
| SMU tool | `ryzenadj` (upstream [FlyGoat/RyzenAdj](https://github.com/FlyGoat/RyzenAdj)) |
| Run length | multi-hour batches |

The workloads never co-exist (VRAM carve-out). The thermal problem is not
overlap — it is that each one, on its own, saturates the SoC package for hours.

---

## 2. The symptom (measured)

Profile: **`balanced`** (the default).

| sensor | reading |
|---|---|
| `k10temp` **Tctl** | ≈ **96 °C** |
| `amdgpu` **edge** | ≈ **98 °C** |
| `acpitz` | ≈ **96 °C** |

Sustained 96–98 °C for hours is unacceptable: it is at/near throttle and
shutdown territory. Without intervention the batch throttles unpredictably or
risks a hard thermal event.

---

## 3. Why the usual "reduce CPU" levers don't work

Tested and **insufficient**:

- `taskset -c 0-3` — confines the process to 4 CPUs.
- `nice -n 19` — lowest scheduling priority.
- `whisper-cli -threads 4` — fewer host decode threads.

None of these solve the problem. Reason: **the model runs on the iGPU**, so CPU
threads only drive host-side glue. The heat source is the **shared SoC
package**, and it is **dominated by the iGPU**. Starving the CPU cores does not
meaningfully lower the package temperature while the iGPU is pegged. (Doubling
host threads from 8 to 16 barely changed decode speed — decoding is GPU-bound.)

Corollary: to cut heat you must touch the **package power/thermal envelope**,
not the CPU schedule.

---

## 4. The `power-saver` trap ⚠️

```sh
powerprofilesctl set power-saver     # works WITHOUT sudo
```

What actually happens:

- `power-saver` sets `power_dpm_force_performance_level = low`.
- That pins the iGPU **sclk to 200 MHz** (out of ~2000 MHz).
- Result: **43–46 °C** — beautifully cool — but **~10× slower**.

For a large batch a 5 h run becomes a **multi-day** run. **Do not use this to
"fix" thermal problems on a big batch.** It trades a thermal risk for a schedule
failure. Use it only for short interactive jobs, or temporarily while
diagnosing.

Related: `powerprofilesctl set balanced` restores full clocks (fast, hot) — the
state the verified fix below is designed to govern.

---

## 5. Verified fix: `ryzenadj`

`ryzenadj` talks to the SMU and lets us set a **package temperature target** and
**power ceilings** directly. The embedded controller (EC) then lowers CPU/GPU
clocks *only as much as needed* and *smoothly* — unlike the all-or-nothing
`power-saver` profile.

### 5.1 The exact command (verified)

```sh
sudo ryzenadj --tctl-temp=85 --stapm-limit=30000 --slow-limit=25000 --fast-limit=35000
```

Output on success:

```
Successfully set ...
no compatible ryzen_smu kernel module found, fallback to /dev/mem
```

The `/dev/mem` fallback message is **expected and fine** on hosts without the
`ryzen_smu` module — `ryzenadj` still applies the limits. "Successfully set"
per limit is the success signal.

### 5.2 What each limit does

Power units are **milliwatts (mW)**.

| flag | value | meaning |
|---|---|---|
| `--tctl-temp` | `85` | Target max **package temperature** (°C). The EC throttles to keep `Tctl ≤ 85` by cutting clocks only as needed. **Primary lever.** |
| `--stapm-limit` | `30000` | Sustained average package power cap (STAPM), 30 W. Bounds long-run heat. |
| `--slow-limit` | `25000` | Slow / sustained PPT ceiling, 25 W (long time constant). |
| `--fast-limit` | `35000` | Fast / burst PPT ceiling, 35 W. Allows short boosts without raising the sustained envelope. |

### 5.3 Result under full load (measured)

| metric | before (balanced, no fix) | after `ryzenadj` |
|---|---|---|
| `k10temp` Tctl | ≈ 96 °C | **63–66 °C** |
| `amdgpu` edge | ≈ 98 °C | **58–65 °C** |
| GPU sclk | 2000 MHz (hot) | **1487–2000 MHz, dynamic** |
| GPU busy | ~90–98 % | **89–98 %** |
| throughput | full (but too hot) | **full — not throttled** |
| package power | — | **~25 W** |
| `THM VALUE CORE` / `THM LIMIT CORE` | — | **~56 °C** / **85** |

The fix is not a compromise: it is **both cool and fast**. That is why it is the
primary mechanism and the watchdog is a backstop.

### 5.4 How to verify (under load)

```sh
ryzenadj -i                  # applied limits + live values
sensors                      # k10temp Tctl, amdgpu edge, acpitz
cat /sys/class/drm/card*/device/pp_dpm_sclk    # current GPU clock state (marker = *)
cat /sys/class/drm/card*/device/gpu_busy_percent
```

Expect `Tctl` in the low-to-mid 60s, `THM LIMIT CORE = 85`, GPU at high sclk and
high busy. If Tctl sits at the limit while clocks are pinned low, lower the
power caps (or raise `--tctl-temp` a little only if you accept the extra heat).

`ryzenadj-governor status` prints all of the above in one shot.

### 5.5 Tuning ranges

| knob | range | default (verified) | effect |
|---|---|---|---|
| `TCTL` | **80–88 °C** | **85** | cooler/slower ↔ hotter/faster |
| `STAPM` | **25–45 W** | **30 W** | sustained average package power |
| `SLOW` | ≈ `STAPM`, 25–45 W | **25 W** | sustained PPT |
| `FAST` | `SLOW` +5–10 W | **35 W** | burst PPT |

Prefer nudging `TCTL` over removing the power caps. Raising the caps makes it
hotter; lowering them makes it slower. The verified set above is a good default.

### 5.6 ⚠️ Volatile — resets on reboot

These values live in the SMU and **reset on every reboot** (and full power
loss). Re-apply after boot (`operations.md` §2).

---

## 6. Rootless fallback: closed-loop thermal watchdog

If root/`ryzenadj` is ever unavailable, the no-root approach is a bang-bang
governor: run hot, then freeze the whole batch process group at a high
temperature and resume once it cooled.

**With the verified `ryzenadj` fix in place this should not trigger** — it is a
backstop only. A conservative `HIGH = 88 °C / LOW = 78 °C` does not fire at the
63–66 °C operating point.

Implementation: `ryzenadj-governor watch` / `ryzenadj-governor run`.

### 6.1 Why `setsid` is mandatory

The watchdog signals a **process group** with `kill -STOP -<PGID>` /
`kill -CONT -<PGID>`. That only works if the batch **is its own process-group
leader** and nothing important shares that group.

- A normal `./batch &` stays in the **shell's** process group; signalling it
  would freeze your shell too.
- `setsid` starts the batch in a **new session** whose process-group id equals
  the new leader's pid. One signal then reaches the batch **and** its children,
  and nothing else.
- `setsid` also lets the batch survive the launching shell.

`ryzenadj-governor run` does this automatically.

### 6.2 Parameters

| parameter | backstop default | meaning |
|---|---|---|
| `--high` | **88 °C** | pause when max sensor ≥ 88 °C |
| `--low` | **78 °C** | resume when max sensor ≤ 78 °C **and** min-pause elapsed |
| `--poll` | 3 s | sensor sampling interval |
| `--min-pause` | 15 s | never resume earlier than this after a pause |

The wide 88/78 hysteresis plus min-pause prevents pause/resume thrash. The tool
watches `k10temp`, `amdgpu` hwmon and every `/sys/class/thermal/thermal_zone*`
(`acpitz`), taking the **max**.

> Historical note: when used as the primary control (before `ryzenadj`) the
> tuning was `HIGH=82 / LOW=66`, giving ~25 % duty cycle and observed peaks
> ~85 °C — cool enough, but with a real throughput cost. That is exactly why
> `ryzenadj` is now primary.

### 6.3 Defect fixed: never leave the batch frozen

A watchdog **killed while the batch was stopped** used to leave the group in
`SIGSTOP` forever — observed once: the batch sat idle in state `T` for ~15 min.
`ryzenadj-governor` now:

- sends `SIGCONT` to the whole group **on startup** (clears any leftover stop),
  and
- sends `SIGCONT` **on exit**, including when terminated by `SIGTERM`/`SIGINT`.

If a batch ever looks frozen (`ps` state `T`), run `kill -CONT -<PGID>`.

### 6.4 Caveats

- Freezing mid-Vulkan is not a designed flow: it resumes in practice on this
  stack, but a driver bug could lose device state. Smooth EC throttling under
  `ryzenadj` avoids this entirely.
- Every pause is lost throughput. The backstop trades speed for safety; the
  verified fix gives you both.

---

## 7. Reproducibility

- **What was measured:** Tctl (`k10temp`), edge/junction (`amdgpu`), ACPI zones
  (`acpitz`), GPU sclk (`pp_dpm_sclk`), GPU busy (`gpu_busy_percent`) and
  `ryzenadj -i` values, sampled during a sustained Vulkan batch.
- **Before/after:** same workload, same power profile (`balanced`), once with no
  limits and once after applying the `ryzenadj` policy above.
- **Stability:** the 63–66 °C figure held over hours at full GPU utilization;
  the GPU did not pin at the temperature limit because the power caps, not the
  clock throttle, did most of the work.
- **Caveat:** exact numbers are host-specific (laptop cooling, ambient). Use the
  *shape* of the fix, then tune `--tctl-temp` and the power caps for your box.

---

## 8. Diagnostics

```sh
# ryzenadj view: applied limits + live SMU values (Tctl, STAPM, THM, ...):
ryzenadj -i

# All sensors (k10temp Tctl, amdgpu edge/junction, acpitz):
sensors

# Raw hwmon temps in millidegrees — same data the watchdog reads:
for f in /sys/class/hwmon/hwmon*/temp*_input; do
  printf '%s = %s C\n' "$f" "$(( $(cat "$f") / 1000 ))"
done

# GPU clock table (marker = current state), busy %, performance level:
cat /sys/class/drm/card*/device/pp_dpm_sclk
cat /sys/class/drm/card*/device/gpu_busy_percent
cat /sys/class/drm/card*/device/power_dpm_force_performance_level

# Current power profile:
powerprofilesctl get

# One-shot summary (this tool):
ryzenadj-governor status
```

---

## 9. Quick reference

| goal | do this |
|---|---|
| **Long batch, root available (default)** | `sudo ryzenadj-governor apply` |
| Re-apply after reboot | `sudo ryzenadj-governor apply` (or systemd unit, see `operations.md`) |
| Verify it works | `ryzenadj-governor status`, `ryzenadj -i`, `sensors` under load |
| Backstop only (no root) | `sudo ryzenadj-governor run -- ./batch ...` or `ryzenadj-governor watch --pgid N` |
| Need it to finish today | **do not** use `power-saver` |
| Short interactive job | `powerprofilesctl set power-saver` is fine |
| Diagnose heat source | `ryzenadj-governor status`; GPU, not CPU, dominates |
| Batch looks frozen (`T`) | `kill -CONT -<PGID>` |

**Rule of thumb:** cap the **package** (temperature + power), not the clock.
`ryzenadj --tctl-temp=85` gives 63–66 °C at full GPU speed; `power-saver` gives
43 °C at 200 MHz (~10× slow); the watchdog is the emergency brake in between.
