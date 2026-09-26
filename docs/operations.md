# Operations runbook

Practical recipes for running a long GPU batch safely on an AMD APU.

---

## 0. Install

**Release binary (recommended):**

```sh
VERSION=v0.1.0
ARCH=amd64   # or arm64
curl -fsSLO "https://github.com/eSlider/ryzenadj/releases/download/${VERSION}/ryzenadj-governor_${VERSION}_linux_${ARCH}.tar.gz"
curl -fsSLO "https://github.com/eSlider/ryzenadj/releases/download/${VERSION}/sha256sums.txt"
sha256sum -c sha256sums.txt --ignore-missing
tar -xzf "ryzenadj-governor_${VERSION}_linux_${ARCH}.tar.gz"
sudo install -m 0755 "ryzenadj-governor_${VERSION}_linux_${ARCH}/ryzenadj-governor" /usr/local/bin/
```

**From source:**

```sh
go install github.com/eSlider/ryzenadj/cmd/ryzenadj-governor@latest
```

You also need the upstream SMU tool for the hardware-limit path:

```sh
# Build/install FlyGoat/RyzenAdj, then:
sudo install -m 0755 ryzenadj /usr/local/bin/ryzenadj
```

---

## 1. Before a long batch (root available) — the normal path

```sh
# 1. Look at the current state.
ryzenadj-governor status

# 2. Apply the verified package policy (Tctl 85 C + power caps).
sudo ryzenadj-governor apply

# 3. Confirm the limits and, under load, the temperatures.
ryzenadj-governor status
```

Expected under full Vulkan load: **Tctl 63–66 °C**, GPU at **1487–2000 MHz**
and high busy. See `thermal-findings.md` §5.

Idempotency: running `apply` again with the same flags is harmless.

---

## 2. Re-apply after reboot (optional systemd unit)

The SMU values reset on every reboot. Either re-run `apply` per batch, or install
a oneshot unit once (needs root):

```ini
# /etc/systemd/system/thermal-policy.service
[Unit]
Description=Apply ryzenadj thermal policy
After=multi-user.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/bin/ryzenadj-governor apply

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now thermal-policy.service
systemctl status thermal-policy.service
```

---

## 3. Run a batch with the watchdog as a backstop

`run` applies the hardware limits when it can and always starts the batch in its
own session, then supervises it:

```sh
ryzenadj-governor run --log /var/log/ryzenadj-governor.log -- \
  ./batch --input /data --threads 16
```

- If you are root and `ryzenadj` is present, the limits are applied first.
- If not, it prints a note and relies on the watchdog.
- The child's exit code is propagated: `echo $?` matches the batch's own code.
- `Ctrl-C` / `SIGTERM` are forwarded to the batch group; the group is always
  `SIGCONT`-ed on the way out.

Tune the backstop:

```sh
ryzenadj-governor run --high 88 --low 78 --poll 3 --min-pause 15 -- ./batch
```

Skip the apply step (watchdog only):

```sh
ryzenadj-governor run --no-apply -- ./batch
```

---

## 4. Supervise an already-running batch

If a batch was launched with `setsid` (or by `run`), find its PGID and attach a
watchdog:

```sh
PGID=$(pgrep -f './batch' | head -1)
PGID=$(ps -o pgid= -p "$PGID" | tr -d ' ')
ryzenadj-governor watch --pgid "$PGID" --log /var/log/ryzenadj-governor.log
```

> The batch **must** be its own process-group leader. If it is not, use
> `run` next time instead — signalling a shared group would freeze the shell too.

---

## 5. Inspect

```sh
ryzenadj-governor status
```

Prints:

- every temperature sensor found (k10temp, amdgpu, acpitz, nvme, …),
- the governor's "watched" maximum,
- per-card GPU performance level, busy % and current sclk,
- `ryzenadj -i` limits when they are readable (root).

---

## 6. Troubleshooting

| symptom | cause | fix |
|---|---|---|
| `apply` says `requires root` | not running as root | `sudo ryzenadj-governor apply` |
| `ryzenadj not found` | upstream binary missing/not in PATH | install RyzenAdj or pass `--ryzenadj /path/to/ryzenadj` |
| batch state is `T` (frozen) | a watchdog died while the group was stopped | `kill -CONT -<PGID>`; new versions self-heal on startup/exit |
| still ~96 °C after `apply` | limits did not take (wrong host/tool) | `ryzenadj -i`; check `status`; consider the watchdog |
| batch much slower, GPU at 200 MHz | `power-saver` profile is active | `powerprofilesctl set balanced` (see `thermal-findings.md` §4) |
| `watch` says group does not exist | wrong PGID or batch exited | re-resolve the PGID |
| `HIGH`/`LOW` rejected | `HIGH <= LOW` or out of range | pick `HIGH > LOW`, both in `(0, 115]` |

---

## 7. Uninstall / clean shutdown

```sh
# Stop a supervised batch: send it a signal; the governor forwards and CONTs.
kill -TERM "$(pgrep -f './batch' | head -1)"

# If you installed the systemd unit:
sudo systemctl disable --now thermal-policy.service
sudo rm /etc/systemd/system/thermal-policy.service
```

There is no daemon and no state on disk except the optional `--log` file.
