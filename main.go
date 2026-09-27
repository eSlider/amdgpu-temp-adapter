// Command amdgpu-temp-adapter manages an AMD Ryzen/APU package thermal policy.
//
// It has three jobs:
//
//   - apply: push a package temperature ceiling (Tctl) plus STAPM/SLOW/FAST
//     power caps into the SMU via ryzenadj. The SMU values are volatile, so
//     this must be re-run after every boot (the shipped
//     amdgpu-temp-adapter.service does exactly that). Needs root (ryzenadj
//     writes /dev/mem).
//   - watchdog: a root-less backstop that SIGSTOPs a process group when the
//     package crosses HIGH and SIGCONTs it below LOW, with a minimum pause.
//     It always CONTs on startup and on TERM/INT so a batch is never left
//     frozen. Start the batch with setsid so it is its own process-group
//     leader, then pass that PGID. This is what keeps long GPU/Vulkan batches
//     (llama.cpp, Whisper, …) from cooking an APU/iGPU shared with the CPU.
//   - temp: print the hottest package sensor in °C (diagnostics, scripting).
//
// The policy is deliberately moderate: Tctl is the primary lever, the power
// caps bound the worst case. The defaults target a Ryzen 7 7700X (Raphael, AM5,
// 8C/16T, TDP 105 W, stock PPT 142 W) but every value is a flag.
//
// This tool shells out to an existing ryzenadj binary; it is not
// FlyGoat/RyzenAdj and does not talk to the SMU itself. For the closed-loop
// governor variant see the sibling project eSlider/ryzenadj.
//
// Usage:
//
//	amdgpu-temp-adapter apply    [-tctl C] [-stapm mW] [-slow mW] [-fast mW] [-ryzenadj PATH] [-dry-run]
//	amdgpu-temp-adapter watchdog -pgid N   [-high C] [-low C] [-poll D] [-min-pause D] [-log PATH] [-sensors CSV]
//	amdgpu-temp-adapter temp     [-sensors CSV] [-json]
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Defaults tuned for the Ryzen 7 7700X (Raphael, AM5, 8C/16T, TDP 105 W,
// stock PPT 142 W). Override with the command flags for other SKUs.
const (
	defaultTctl     = 85
	defaultSTAPM    = 88000
	defaultSlow     = 76000
	defaultFast     = 105000
	defaultHigh     = 88
	defaultLow      = 78
	defaultPoll     = 3 * time.Second
	defaultMinPause = 15 * time.Second
	defaultSensors  = "k10temp,amdgpu"
	defaultRyzenadj = "/usr/local/bin/ryzenadj"
)

// fsys abstracts the filesystem so the sensor scan is unit-testable without
// real hwmon devices.
type fsys struct {
	glob func(pattern string) []string
	read func(path string) string
}

func osFS() fsys {
	return fsys{
		glob: func(p string) []string {
			m, _ := filepath.Glob(p)
			sort.Strings(m)
			return m
		},
		read: func(p string) string {
			b, err := os.ReadFile(p)
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		},
	}
}

// millisToC converts a sysfs millidegree reading to whole °C. Unparsable or
// negative readings become 0 (treated as "no data").
func millisToC(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	return n / 1000
}

func splitCSV(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// maxTemp returns the hottest temperature in °C across the selected hwmon
// chips (k10temp = Ryzen Tctl/Tccd, amdgpu = iGPU) plus every thermal zone
// (acpitz). 0 means no sensor was readable.
func maxTemp(f fsys, sensors []string) int {
	want := make(map[string]bool, len(sensors))
	for _, s := range sensors {
		want[s] = true
	}
	max := 0
	for _, dir := range f.glob("/sys/class/hwmon/hwmon*") {
		if !want[f.read(filepath.Join(dir, "name"))] {
			continue
		}
		for _, p := range f.glob(filepath.Join(dir, "temp*_input")) {
			if v := millisToC(f.read(p)); v > max {
				max = v
			}
		}
	}
	for _, p := range f.glob("/sys/class/thermal/thermal_zone*/temp") {
		if v := millisToC(f.read(p)); v > max {
			max = v
		}
	}
	return max
}

type decision int

const (
	decideNone decision = iota
	decideStop
	decideCont
)

// decide is the pure hysteresis state machine. pausedFor (seconds in the
// paused state) only matters while paused; it enforces the minimum pause so a
// batch cannot be resumed immediately and thrash.
func decide(temp int, paused bool, pausedFor, minPause, high, low int) decision {
	if !paused {
		if temp >= high {
			return decideStop
		}
		return decideNone
	}
	if temp <= low && pausedFor >= minPause {
		return decideCont
	}
	return decideNone
}

func buildRyzenadjArgs(tctl, stapm, slow, fast int) []string {
	return []string{
		fmt.Sprintf("--tctl-temp=%d", tctl),
		fmt.Sprintf("--stapm-limit=%d", stapm),
		fmt.Sprintf("--slow-limit=%d", slow),
		fmt.Sprintf("--fast-limit=%d", fast),
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "amdgpu-temp-adapter: "+format+"\n", args...)
	os.Exit(1)
}

func cmdApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	tctl := fs.Int("tctl", defaultTctl, "package temperature ceiling (°C)")
	stapm := fs.Int("stapm", defaultSTAPM, "STAPM sustained power cap (mW)")
	slow := fs.Int("slow", defaultSlow, "slow/sustained PPT cap (mW)")
	fast := fs.Int("fast", defaultFast, "fast/burst PPT cap (mW)")
	radj := fs.String("ryzenadj", defaultRyzenadj, "path to the ryzenadj binary")
	dry := fs.Bool("dry-run", false, "print the command instead of running it")
	_ = fs.Parse(args)

	cmdArgs := buildRyzenadjArgs(*tctl, *stapm, *slow, *fast)
	fmt.Printf("amdgpu-temp-adapter apply: %s %s\n", *radj, strings.Join(cmdArgs, " "))
	if *dry {
		return
	}
	if _, err := os.Stat(*radj); err != nil {
		fatalf("ryzenadj not found at %s: %v", *radj, err)
	}
	out, err := exec.Command(*radj, cmdArgs...).CombinedOutput()
	fmt.Print(string(out))
	if err != nil {
		fatalf("ryzenadj failed: %v", err)
	}
	// ryzenadj also exits 0 when it could not read the power-metric table
	// (expected on some families); the per-limit "Successfully set" lines are
	// the real success signal.
	if !strings.Contains(string(out), "Successfully set") {
		fatalf("ryzenadj did not report a successful set")
	}
	fmt.Printf("OK: Tctl<=%d°C, STAPM=%dmW, SLOW=%dmW, FAST=%dmW (volatile; re-apply after reboot)\n",
		*tctl, *stapm, *slow, *fast)
}

func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func cmdWatchdog(args []string) {
	fs := flag.NewFlagSet("watchdog", flag.ExitOnError)
	pgid := fs.Int("pgid", 0, "process-group id to govern (required)")
	high := fs.Int("high", defaultHigh, "pause the group at or above this temperature (°C)")
	low := fs.Int("low", defaultLow, "resume the group at or below this temperature (°C)")
	poll := fs.Duration("poll", defaultPoll, "sensor poll interval")
	minPause := fs.Duration("min-pause", defaultMinPause, "minimum time to keep the group paused")
	logPath := fs.String("log", "", "append events to this file (default: stderr)")
	sensorsCSV := fs.String("sensors", defaultSensors, "hwmon chip names to include (CSV)")
	_ = fs.Parse(args)

	if *pgid <= 0 {
		fatalf("watchdog: -pgid must be a positive process-group id")
	}
	if *low >= *high {
		fatalf("watchdog: -low (%d) must be below -high (%d)", *low, *high)
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fatalf("watchdog: open log: %v", err)
		}
		defer f.Close()
		logger = log.New(f, "", log.LstdFlags)
	}

	sensors := splitCSV(*sensorsCSV)
	sysfs := osFS()

	// SAFETY: a previous watchdog may have been killed while the group was
	// STOPped, leaving the batch frozen forever. Always clear that on startup
	// and again before we exit on TERM/INT.
	if err := syscall.Kill(-*pgid, syscall.SIGCONT); err == nil {
		logger.Printf("startup CONT pgid=%d", *pgid)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)

	logger.Printf("watchdog start pgid=%d HIGH=%dC LOW=%dC poll=%s min_pause=%s",
		*pgid, *high, *low, *poll, *minPause)

	ticker := time.NewTicker(*poll)
	defer ticker.Stop()

	paused := false
	var pauseStart time.Time

	contOnExit := func(reason string) {
		if paused {
			if err := syscall.Kill(-*pgid, syscall.SIGCONT); err == nil {
				logger.Printf("%s; CONT pgid=%d", reason, *pgid)
			}
		} else {
			logger.Printf("%s", reason)
		}
	}

	for {
		select {
		case s := <-sig:
			contOnExit(fmt.Sprintf("signal %s; exiting", s))
			return
		case <-ticker.C:
		}

		if !groupAlive(*pgid) {
			contOnExit(fmt.Sprintf("group pgid=%d gone; exiting", *pgid))
			return
		}

		t := maxTemp(sysfs, sensors)
		switch decide(t, paused, int(time.Since(pauseStart).Seconds()), int(minPause.Seconds()), *high, *low) {
		case decideStop:
			if err := syscall.Kill(-*pgid, syscall.SIGSTOP); err == nil {
				paused, pauseStart = true, time.Now()
				logger.Printf("PAUSE  t=%dC pgid=%d", t, *pgid)
			}
		case decideCont:
			if err := syscall.Kill(-*pgid, syscall.SIGCONT); err == nil {
				logger.Printf("RESUME t=%dC (paused %s)", t, time.Since(pauseStart).Round(time.Second))
				paused = false
			}
		}
	}
}

func cmdTemp(args []string) {
	fs := flag.NewFlagSet("temp", flag.ExitOnError)
	sensorsCSV := fs.String("sensors", defaultSensors, "hwmon chip names to include (CSV)")
	asJSON := fs.Bool("json", false, "emit JSON")
	_ = fs.Parse(args)

	t := maxTemp(osFS(), splitCSV(*sensorsCSV))
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"max_c": t})
		return
	}
	fmt.Printf("%d\n", t)
}

func usage() {
	fmt.Fprint(os.Stderr, `amdgpu-temp-adapter — AMD Ryzen/APU package thermal policy

usage:
  amdgpu-temp-adapter apply    [-tctl C] [-stapm mW] [-slow mW] [-fast mW] [-ryzenadj PATH] [-dry-run]
  amdgpu-temp-adapter watchdog -pgid N [-high C] [-low C] [-poll D] [-min-pause D] [-log PATH] [-sensors CSV]
  amdgpu-temp-adapter temp     [-sensors CSV] [-json]

defaults: tctl=85C stapm=88000mW slow=76000mW fast=105000mW; watchdog high=88C low=78C
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "apply":
		cmdApply(os.Args[2:])
	case "watchdog":
		cmdWatchdog(os.Args[2:])
	case "temp":
		cmdTemp(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "amdgpu-temp-adapter: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}
