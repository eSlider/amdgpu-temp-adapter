// Package watchdog implements the closed-loop thermal governor: a root-less
// bang-bang controller that SIGSTOPs a process group when the SoC gets too hot
// and SIGCONTs it once it has cooled below a lower threshold.
//
// The hysteresis state machine is pure and unit-tested; the signalling loop is
// Linux-specific.
package watchdog

import (
	"errors"
	"fmt"
	"time"
)

// Config tunes the controller.
type Config struct {
	// HighC pauses the watched group when the hottest sensor reaches this.
	HighC int
	// LowC resumes when the hottest sensor drops to/below this and MinPause
	// has elapsed. Must be strictly below HighC.
	LowC int
	// Poll is the sensor sampling interval.
	Poll time.Duration
	// MinPause is the minimum time to keep the group paused before resuming.
	// Prevents pause/resume thrash that is both slow and risky for GPU
	// contexts.
	MinPause time.Duration
}

// DefaultConfig is the conservative backstop used when hardware limits are not
// available. With `ryzenadj --tctl-temp` in place this should never fire.
func DefaultConfig() Config {
	return Config{HighC: 88, LowC: 78, Poll: 3 * time.Second, MinPause: 15 * time.Second}
}

// Validate checks the configuration for sane, non-thrashing thresholds.
func (c Config) Validate() error {
	if c.HighC <= 0 || c.HighC > 115 {
		return fmt.Errorf("high temperature %d out of range (0, 115]", c.HighC)
	}
	if c.LowC <= 0 || c.LowC > 115 {
		return fmt.Errorf("low temperature %d out of range (0, 115]", c.LowC)
	}
	if c.HighC <= c.LowC {
		return fmt.Errorf("high temperature (%dC) must be greater than low temperature (%dC)", c.HighC, c.LowC)
	}
	if c.Poll <= 0 {
		return errors.New("poll interval must be positive")
	}
	if c.MinPause < 0 {
		return errors.New("min-pause must not be negative")
	}
	return nil
}

// Decision is the action the controller wants to take this tick.
type Decision int

const (
	// Noop means keep the current state.
	Noop Decision = iota
	// Pause means SIGSTOP the watched group.
	Pause
	// Resume means SIGCONT the watched group.
	Resume
)

func (d Decision) String() string {
	switch d {
	case Pause:
		return "PAUSE"
	case Resume:
		return "RESUME"
	default:
		return "NOOP"
	}
}

// Machine is the hysteresis state machine. It is not safe for concurrent use;
// drive it from a single goroutine.
type Machine struct {
	cfg          Config
	paused       bool
	pauseStarted time.Time
}

// New builds a validated state machine.
func New(cfg Config) (*Machine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Machine{cfg: cfg}, nil
}

// Config returns the machine's configuration.
func (m *Machine) Config() Config { return m.cfg }

// Paused reports whether the machine currently considers the group paused.
func (m *Machine) Paused() bool { return m.paused }

// Update feeds a temperature sample and the current time, returning the
// decision for this tick.
//
//   - not paused and temp >= High  -> Pause
//   - paused, temp <= Low, and at least MinPause elapsed -> Resume
//   - otherwise -> Noop
func (m *Machine) Update(tempC int, now time.Time) Decision {
	switch {
	case !m.paused && tempC >= m.cfg.HighC:
		m.paused = true
		m.pauseStarted = now
		return Pause
	case m.paused && tempC <= m.cfg.LowC && now.Sub(m.pauseStarted) >= m.cfg.MinPause:
		m.paused = false
		return Resume
	default:
		return Noop
	}
}

// ForceResume clears the paused state without a temperature sample. It is used
// on shutdown so a stopped group is never left behind.
func (m *Machine) ForceResume() {
	m.paused = false
}
