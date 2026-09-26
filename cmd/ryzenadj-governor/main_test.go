package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunDispatch(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"unknown command", []string{"frobnicate"}, 2},
		{"version", []string{"version"}, 0},
		{"help", []string{"help"}, 0},
		{"apply dry-run", []string{"apply", "--dry-run"}, 0},
		{"bad limits", []string{"apply", "--dry-run", "--tctl-temp=500"}, 1},
		{"watch missing pgid", []string{"watch"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if got := run(c.args, &out, &errBuf); got != c.want {
				t.Fatalf("run(%v) = %d, want %d (stderr: %s)", c.args, got, c.want, errBuf.String())
			}
		})
	}
}

func TestVersionOutput(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"version"}, &out, &errBuf); code != 0 {
		t.Fatalf("version exited %d", code)
	}
	if !strings.Contains(out.String(), "ryzenadj-governor") {
		t.Errorf("version output = %q", out.String())
	}
}

func TestDryRunPrintsPolicy(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{"apply", "--dry-run"}, &out, &errBuf); code != 0 {
		t.Fatalf("dry-run exited %d", code)
	}
	for _, want := range []string{"--tctl-temp=85", "--stapm-limit=30000", "--slow-limit=25000", "--fast-limit=35000"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output missing %q: %s", want, out.String())
		}
	}
}
