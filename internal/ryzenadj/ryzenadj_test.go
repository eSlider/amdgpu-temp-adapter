package ryzenadj

import (
	"strings"
	"testing"
)

func TestDefaultLimitsValid(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatalf("DefaultLimits().Validate() = %v", err)
	}
}

func TestValidate(t *testing.T) {
	valid := DefaultLimits()
	cases := []struct {
		name    string
		mutate  func(*Limits)
		wantErr bool
	}{
		{"default", func(*Limits) {}, false},
		{"tctl too low", func(l *Limits) { l.TctlTemp = MinTctlTemp - 1 }, true},
		{"tctl too high", func(l *Limits) { l.TctlTemp = MaxTctlTemp + 1 }, true},
		{"stapm too low", func(l *Limits) { l.StapmLimit = MinStapm - 1 }, true},
		{"slow too high", func(l *Limits) { l.SlowLimit = MaxSlow + 1 }, true},
		{"fast too high", func(l *Limits) { l.FastLimit = MaxFast + 1 }, true},
		{"fast below slow", func(l *Limits) { l.SlowLimit = 40000; l.FastLimit = 35000 }, true},
		{"fast equals slow", func(l *Limits) { l.SlowLimit = 35000; l.FastLimit = 35000 }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := valid
			c.mutate(&l)
			err := l.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestLimitsArgs(t *testing.T) {
	got := DefaultLimits().Args()
	want := []string{"--tctl-temp=85", "--stapm-limit=30000", "--slow-limit=25000", "--fast-limit=35000"}
	if len(got) != len(want) {
		t.Fatalf("Args() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Args()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if s := DefaultLimits().String(); !strings.Contains(s, "--tctl-temp=85") {
		t.Errorf("String() = %q, missing tctl", s)
	}
}

func TestFindExplicitMissing(t *testing.T) {
	if _, err := Find("/nonexistent/ryzenadj-xyz"); err == nil {
		t.Fatal("expected error for missing explicit path")
	}
}

func TestParseInfo(t *testing.T) {
	// Shape mirrors `ryzenadj -i` output closely enough for the parser.
	output := `CPU Family: Cezanne
SMU BIOS Interface Version: 22
Version: v0.14.0
PM Table Version: 4d0009
      Name         Value        Parameter
-----------------------------------------
STAPM LIMIT           30000.000  stapm_limit
STAPM VALUE              23.456  stapm_value
THM LIMIT CORE           85.000  thm_limit_core
THM VALUE CORE           56.000  thm_value_core
`
	if v, ok := ParseInfo(output, "STAPM LIMIT"); !ok || v != 30000 {
		t.Errorf("STAPM LIMIT = %d, %v; want 30000, true", v, ok)
	}
	if v, ok := ParseInfo(output, "THM LIMIT CORE"); !ok || v != 85 {
		t.Errorf("THM LIMIT CORE = %d, %v; want 85, true", v, ok)
	}
	if _, ok := ParseInfo(output, "NO SUCH KEY"); ok {
		t.Error("expected ok=false for unknown key")
	}
}
