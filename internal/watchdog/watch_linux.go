//go:build linux

package watchdog

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

// Signal sends sig to an entire process group. A negative pid targets the
// group. It is exported so callers can perform the mandatory startup/shutdown
// CONT themselves.
func Signal(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return fmt.Errorf("invalid pgid %d", pgid)
	}
	if err := syscall.Kill(-pgid, sig); err != nil {
		return fmt.Errorf("signal %v to pgid %d: %w", sig, pgid, err)
	}
	return nil
}

// Cont resumes a stopped process group. Safe to call when already running.
func Cont(pgid int) error { return Signal(pgid, syscall.SIGCONT) }

// Stop freezes a process group.
func Stop(pgid int) error { return Signal(pgid, syscall.SIGSTOP) }

// Alive reports whether the process group still has at least one member.
func Alive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	return syscall.Kill(-pgid, 0) == nil
}

// Watcher runs the closed-loop controller against one process group.
type Watcher struct {
	// Config tunes thresholds and timing.
	Config Config
	// PGID is the target process-group id.
	PGID int
	// Temp returns the current hottest temperature and whether a reading was
	// available. It is injected so the loop is testable and so callers can
	// choose which sensors count.
	Temp func() (int, bool)
	// Logf receives structured event lines. If nil, events are dropped.
	Logf func(format string, args ...any)
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

// Run drives the control loop until the context is cancelled or the watched
// group disappears. It always sends SIGCONT before returning, even when
// cancelled mid-pause, so the group is never left frozen.
func (w *Watcher) Run(ctx context.Context) error {
	if w.PGID <= 0 {
		return fmt.Errorf("invalid pgid %d", w.PGID)
	}
	if w.Temp == nil {
		return fmt.Errorf("temp function is nil")
	}
	m, err := New(w.Config)
	if err != nil {
		return err
	}

	// SAFETY: a previous watchdog may have died while the group was stopped,
	// leaving it frozen. Always clear that first.
	if err := Cont(w.PGID); err != nil {
		return fmt.Errorf("initial CONT: %w", err)
	}
	w.logf("watchdog start pgid=%d HIGH=%dC LOW=%dC poll=%s min_pause=%s",
		w.PGID, w.Config.HighC, w.Config.LowC, w.Config.Poll, w.Config.MinPause)

	defer func() {
		// SAFETY: never leave the group frozen on the way out.
		if err := Cont(w.PGID); err == nil {
			w.logf("watchdog exit; CONT sent")
		}
	}()

	ticker := time.NewTicker(w.Config.Poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			if !Alive(w.PGID) {
				w.logf("batch gone; watchdog exit")
				return nil
			}
			temp, ok := w.Temp()
			if !ok {
				continue
			}
			switch m.Update(temp, now) {
			case Pause:
				if err := Stop(w.PGID); err != nil {
					w.logf("PAUSE t=%dC failed: %v", temp, err)
					m.ForceResume()
					continue
				}
				w.logf("PAUSE  t=%dC", temp)
			case Resume:
				if err := Cont(w.PGID); err != nil {
					w.logf("RESUME t=%dC failed: %v", temp, err)
					continue
				}
				w.logf("RESUME t=%dC", temp)
			}
		}
	}
}
