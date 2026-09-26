//go:build linux

// Command ryzenadj-governor keeps an AMD APU cool during long GPU batches.
//
// It has two independent mechanisms, usable together or separately:
//
//   - a hardware thermal/power policy applied through the external `ryzenadj`
//     tool (see the `apply` subcommand), and
//   - a root-less closed-loop watchdog that SIGSTOPs/SIGCONTs a process group
//     around temperature thresholds (see `watch` and `run`).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/eSlider/ryzenadj/internal/governor"
	"github.com/eSlider/ryzenadj/internal/ryzenadj"
	"github.com/eSlider/ryzenadj/internal/thermal"
	"github.com/eSlider/ryzenadj/internal/watchdog"
)

// Build metadata, overridable with -ldflags "-X main.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "apply":
		err = cmdApply(args[1:], stdout, stderr)
	case "watch":
		err = cmdWatch(args[1:], stdout, stderr)
	case "run":
		err = cmdRun(args[1:], stdout, stderr)
	case "status":
		err = cmdStatus(args[1:], stdout)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "ryzenadj-governor %s (commit %s, built %s, %s)\n", Version, Commit, Date, runtime.Version())
	case "help", "-h", "--help":
		usage(stdout)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		var ec exitCodeError
		if errors.As(err, &ec) {
			return int(ec)
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `ryzenadj-governor - thermal governor for AMD APUs (Ryzen 7 5800H / gfx90c)

Usage:
  ryzenadj-governor <command> [flags]

Commands:
  apply     Apply a hardware thermal/power policy via the external ryzenadj (root)
  watch     Root-less closed-loop watchdog for an existing process group
  run       Apply limits (best effort), run a command in its own group and watch it
  status    Show temperatures, GPU clock/busy and ryzenadj limits
  version   Print build metadata

Run "ryzenadj-governor <command> --help" for command-specific flags.

This project is a separate tool from FlyGoat/RyzenAdj. It shells out to an
existing ryzenadj binary; it does not talk to the SMU itself.
`)
}

// limitsFlags registers the shared ryzenadj policy flags on fs.
func limitsFlags(fs *flag.FlagSet, def ryzenadj.Limits) (tctl, stapm, slow, fast *int) {
	tctl = fs.Int("tctl-temp", def.TctlTemp, "package temperature target in C")
	stapm = fs.Int("stapm-limit", def.StapmLimit, "sustained average package power cap in mW")
	slow = fs.Int("slow-limit", def.SlowLimit, "slow/sustained PPT ceiling in mW")
	fast = fs.Int("fast-limit", def.FastLimit, "fast/burst PPT ceiling in mW")
	return
}

func buildLimits(tctl, stapm, slow, fast int) (ryzenadj.Limits, error) {
	l := ryzenadj.Limits{TctlTemp: tctl, StapmLimit: stapm, SlowLimit: slow, FastLimit: fast}
	if err := l.Validate(); err != nil {
		return ryzenadj.Limits{}, err
	}
	return l, nil
}

func cmdApply(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "usage: ryzenadj-governor apply [flags]") }
	def := ryzenadj.DefaultLimits()
	tctl, stapm, slow, fast := limitsFlags(fs, def)
	bin := fs.String("ryzenadj", "", "path to the ryzenadj binary (default: search PATH and /usr/local/bin)")
	dryRun := fs.Bool("dry-run", false, "print the policy without applying it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	limits, err := buildLimits(*tctl, *stapm, *slow, *fast)
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Fprintf(stdout, "dry-run: would run: %s %s\n", orDefault(*bin, "ryzenadj"), limits)
		return nil
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("apply requires root (try: sudo %s apply)", "ryzenadj-governor")
	}
	path, err := ryzenadj.Find(*bin)
	if err != nil {
		return err
	}

	fmt.Fprintln(stdout, "==================== BEFORE ====================")
	printInfo(stdout, path)
	if err := ryzenadj.Apply(context.Background(), path, limits, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "==================== AFTER =====================")
	printInfo(stdout, path)
	fmt.Fprintf(stdout, "\nOK: %s applied (idempotent; resets on reboot)\n", limits)
	return nil
}

func printInfo(w io.Writer, bin string) {
	info, err := ryzenadj.Info(context.Background(), bin)
	if err != nil {
		fmt.Fprintf(w, "ryzenadj -i unavailable: %v\n", err)
	}
	fmt.Fprint(w, info)
	if info != "" && info[len(info)-1] != '\n' {
		fmt.Fprintln(w)
	}
}

// watchFlags registers the shared watchdog flags. The returned pointers are
// only valid after fs.Parse; call makeWatchConfig to assemble the config.
func watchFlags(fs *flag.FlagSet) (high, low, pollSec, minPauseSec *int) {
	def := watchdog.DefaultConfig()
	high = fs.Int("high", def.HighC, "pause when the hottest watched sensor reaches this C")
	low = fs.Int("low", def.LowC, "resume when the hottest watched sensor drops to this C")
	pollSec = fs.Int("poll", int(def.Poll.Seconds()), "sensor sampling interval in seconds")
	minPauseSec = fs.Int("min-pause", int(def.MinPause.Seconds()), "minimum time to keep a group paused, in seconds")
	return
}

func makeWatchConfig(high, low, pollSec, minPauseSec int) watchdog.Config {
	return watchdog.Config{
		HighC:    high,
		LowC:     low,
		Poll:     time.Duration(pollSec) * time.Second,
		MinPause: time.Duration(minPauseSec) * time.Second,
	}
}

func cmdWatch(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "usage: ryzenadj-governor watch --pgid N [flags]") }
	pgid := fs.Int("pgid", 0, "process-group id to watch (required)")
	high, low, poll, minPause := watchFlags(fs)
	logPath := fs.String("log", "", "append event log to this file (in addition to stderr)")
	sysfs := fs.String("sysfs", thermal.DefaultSysfs, "sysfs root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pgid <= 0 {
		return fmt.Errorf("--pgid is required and must be positive")
	}
	cfg := makeWatchConfig(*high, *low, *poll, *minPause)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !watchdog.Alive(*pgid) {
		return fmt.Errorf("process group %d does not exist", *pgid)
	}

	logger, closeLog, err := newEventLogger(stderr, *logPath)
	if err != nil {
		return err
	}
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(stdout, "watching pgid %d: HIGH=%dC LOW=%dC poll=%s min-pause=%s\n",
		*pgid, cfg.HighC, cfg.LowC, cfg.Poll, cfg.MinPause)
	w := &watchdog.Watcher{
		Config: cfg,
		PGID:   *pgid,
		Temp:   tempFunc(*sysfs),
		Logf:   logger.Printf,
	}
	if err := w.Run(ctx); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "watchdog stopped")
	return nil
}

func cmdStatus(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stdout)
	bin := fs.String("ryzenadj", "", "path to the ryzenadj binary")
	sysfs := fs.String("sysfs", thermal.DefaultSysfs, "sysfs root")
	if err := fs.Parse(args); err != nil {
		return err
	}

	info := ""
	if path, err := ryzenadj.Find(*bin); err == nil {
		if out, err := ryzenadj.Info(context.Background(), path); err == nil {
			info = out
		} else {
			info = out
		}
	}
	return governor.Status(stdout, *sysfs, info)
}

func cmdRun(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: ryzenadj-governor run [flags] -- <command> [args...]")
	}
	high, low, poll, minPause := watchFlags(fs)
	bin := fs.String("ryzenadj", "", "path to the ryzenadj binary")
	noApply := fs.Bool("no-apply", false, "do not try to apply ryzenadj limits")
	logPath := fs.String("log", "", "append event log to this file (in addition to stderr)")
	sysfs := fs.String("sysfs", thermal.DefaultSysfs, "sysfs root")
	def := ryzenadj.DefaultLimits()
	tctl, stapm, slow, fast := limitsFlags(fs, def)
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if len(command) == 0 {
		return fmt.Errorf("no command given; use: ryzenadj-governor run -- <command> [args...]")
	}
	cfg := makeWatchConfig(*high, *low, *poll, *minPause)
	if err := cfg.Validate(); err != nil {
		return err
	}
	limits, err := buildLimits(*tctl, *stapm, *slow, *fast)
	if err != nil {
		return err
	}

	logger, closeLog, err := newEventLogger(stderr, *logPath)
	if err != nil {
		return err
	}
	defer closeLog()

	applyPolicy(stdout, stderr, logger, *bin, limits, *noApply)

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Setsid makes the child its own session and process-group leader, so one
	// signal reaches it and all of its children and nothing else.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %q: %w", command[0], err)
	}
	pgid := cmd.Process.Pid
	logger.Printf("started %q pid=%d pgid=%d", command[0], cmd.Process.Pid, pgid)
	fmt.Fprintf(stdout, "started %q (pid %d, pgid %d); watching HIGH=%dC LOW=%dC\n",
		command[0], cmd.Process.Pid, pgid, cfg.HighC, cfg.LowC)

	watchCtx, stopWatch := context.WithCancel(context.Background())
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		w := &watchdog.Watcher{Config: cfg, PGID: pgid, Temp: tempFunc(*sysfs), Logf: logger.Printf}
		if err := w.Run(watchCtx); err != nil {
			logger.Printf("watchdog error: %v", err)
		}
	}()

	// Forward termination signals to the child group: because the child is in
	// its own session it does not receive terminal signals directly. The
	// watchdog's defer sends CONT, so the group is never left frozen.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigCh)
	go func() {
		for sig := range sigCh {
			logger.Printf("forwarding %v to pgid %d", sig, pgid)
			_ = watchdog.Signal(pgid, sig.(syscall.Signal))
		}
	}()

	waitErr := cmd.Wait()
	stopWatch()
	<-watchDone
	_ = watchdog.Cont(pgid) // belt and braces

	code := exitCode(waitErr)
	logger.Printf("command exited with code %d", code)
	if code == 0 {
		return nil
	}
	return exitCodeError(code)
}

// applyPolicy applies ryzenadj limits when possible and never blocks the run on
// failure: the root-less watchdog is the fallback.
func applyPolicy(stdout, stderr io.Writer, logger *log.Logger, bin string, limits ryzenadj.Limits, noApply bool) {
	if noApply {
		logger.Printf("skipping ryzenadj apply (--no-apply)")
		return
	}
	if os.Geteuid() != 0 {
		logger.Printf("not root: skipping ryzenadj apply (watchdog only)")
		fmt.Fprintln(stderr, "note: not root, skipping ryzenadj limits; watchdog is the only protection")
		return
	}
	path, err := ryzenadj.Find(bin)
	if err != nil {
		logger.Printf("ryzenadj unavailable: %v", err)
		return
	}
	logger.Printf("applying ryzenadj limits: %s", limits)
	if err := ryzenadj.Apply(context.Background(), path, limits, stdout); err != nil {
		logger.Printf("ryzenadj apply failed: %v", err)
		fmt.Fprintf(stderr, "warning: could not apply ryzenadj limits: %v\n", err)
	}
}

// exitCode maps a Wait error to a shell exit code (128+signal when killed).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

// exitCodeError lets cmdRun propagate the child's code through run().
type exitCodeError int

func (e exitCodeError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func tempFunc(sysfs string) func() (int, bool) {
	return func() (int, bool) {
		readings, err := thermal.Scan(sysfs)
		if err != nil {
			return 0, false
		}
		return thermal.MaxCelsius(readings, thermal.SensorMatchers...)
	}
}

// newEventLogger writes events to stderr and, optionally, an append-only file.
func newEventLogger(stderr io.Writer, path string) (*log.Logger, func(), error) {
	w := io.Writer(stderr)
	closeFn := func() {}
	if path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open log %q: %w", path, err)
		}
		w = io.MultiWriter(stderr, f)
		closeFn = func() { _ = f.Close() }
	}
	return log.New(w, "", log.LstdFlags), closeFn, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
