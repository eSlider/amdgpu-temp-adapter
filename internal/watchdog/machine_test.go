package watchdog

import (
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"default", DefaultConfig(), false},
		{"high below low", Config{HighC: 70, LowC: 80, Poll: time.Second}, true},
		{"high equals low", Config{HighC: 80, LowC: 80, Poll: time.Second}, true},
		{"high too high", Config{HighC: 200, LowC: 80, Poll: time.Second}, true},
		{"zero poll", Config{HighC: 88, LowC: 78}, true},
		{"negative min pause", Config{HighC: 88, LowC: 78, Poll: time.Second, MinPause: -1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestMachineHysteresis(t *testing.T) {
	cfg := Config{HighC: 88, LowC: 78, Poll: time.Second, MinPause: 15 * time.Second}
	m, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t0 := time.Unix(0, 0)

	// Below HIGH: stay running.
	if d := m.Update(80, t0); d != Noop || m.Paused() {
		t.Fatalf("at 80C: decision=%v paused=%v; want NOOP/running", d, m.Paused())
	}
	// Exactly HIGH: pause.
	if d := m.Update(88, t0.Add(time.Second)); d != Pause || !m.Paused() {
		t.Fatalf("at 88C: decision=%v paused=%v; want PAUSE/paused", d, m.Paused())
	}
	// Cooled but not enough and min-pause not elapsed: stay paused.
	if d := m.Update(70, t0.Add(5*time.Second)); d != Noop || !m.Paused() {
		t.Fatalf("after 5s at 70C: decision=%v paused=%v; want NOOP/paused (min-pause)", d, m.Paused())
	}
	// Still above LOW after min-pause: stay paused.
	if d := m.Update(85, t0.Add(30*time.Second)); d != Noop || !m.Paused() {
		t.Fatalf("at 85C after 30s: decision=%v paused=%v; want NOOP/paused", d, m.Paused())
	}
	// Exactly LOW after min-pause: resume.
	if d := m.Update(78, t0.Add(45*time.Second)); d != Resume || m.Paused() {
		t.Fatalf("at 78C after 45s: decision=%v paused=%v; want RESUME/running", d, m.Paused())
	}
	// Hot again: pause again.
	if d := m.Update(90, t0.Add(46*time.Second)); d != Pause || !m.Paused() {
		t.Fatalf("at 90C: decision=%v paused=%v; want PAUSE/paused", d, m.Paused())
	}
}

func TestForceResume(t *testing.T) {
	m, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(0, 0)
	if d := m.Update(95, now); d != Pause {
		t.Fatalf("expected PAUSE, got %v", d)
	}
	m.ForceResume()
	if m.Paused() {
		t.Fatal("ForceResume did not clear paused state")
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	if _, err := New(Config{HighC: 10, LowC: 20, Poll: time.Second}); err == nil {
		t.Fatal("expected error for high <= low")
	}
}
