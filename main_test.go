package main

import (
	"reflect"
	"testing"
)

func TestMillisToC(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"42875", 42},
		{"85000\n", 85},
		{"0", 0},
		{"-5000", 0},
		{"", 0},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := millisToC(c.in); got != c.want {
			t.Errorf("millisToC(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	got := splitCSV(" k10temp , amdgpu ,, acpitz ")
	want := []string{"k10temp", "amdgpu", "acpitz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitCSV = %v, want %v", got, want)
	}
	if got := splitCSV(""); got != nil {
		t.Fatalf("splitCSV(\"\") = %v, want nil", got)
	}
}

func fakeFS(files map[string]string, globs map[string][]string) fsys {
	return fsys{
		glob: func(p string) []string { return globs[p] },
		read: func(p string) string { return files[p] },
	}
}

func TestMaxTemp(t *testing.T) {
	files := map[string]string{
		"/hwmon/hwmon2/name":          "k10temp",
		"/hwmon/hwmon2/temp1_input":   "61200",
		"/hwmon/hwmon2/temp3_input":   "58400",
		"/hwmon/hwmon8/name":          "amdgpu",
		"/hwmon/hwmon8/temp1_input":   "64000",
		"/hwmon/hwmon4/name":          "xe",
		"/hwmon/hwmon4/temp1_input":   "99000", // must be ignored (not a SoC sensor)
		"/thermal/thermal_zone0/temp": "70000",
	}
	globs := map[string][]string{
		"/sys/class/hwmon/hwmon*":               {"/hwmon/hwmon2", "/hwmon/hwmon4", "/hwmon/hwmon8"},
		"/hwmon/hwmon2/temp*_input":             {"/hwmon/hwmon2/temp1_input", "/hwmon/hwmon2/temp3_input"},
		"/hwmon/hwmon4/temp*_input":             {"/hwmon/hwmon4/temp1_input"},
		"/hwmon/hwmon8/temp*_input":             {"/hwmon/hwmon8/temp1_input"},
		"/sys/class/thermal/thermal_zone*/temp": {"/thermal/thermal_zone0/temp"},
	}
	// max over k10temp (61), amdgpu (64), zone (70); xe (99) excluded.
	if got := maxTemp(fakeFS(files, globs), []string{"k10temp", "amdgpu"}); got != 70 {
		t.Fatalf("maxTemp = %d, want 70", got)
	}
	if got := maxTemp(fakeFS(files, globs), []string{"k10temp"}); got != 70 {
		t.Fatalf("maxTemp (thermal zone still counts) = %d, want 70", got)
	}
	if got := maxTemp(fakeFS(map[string]string{}, map[string][]string{}), []string{"k10temp"}); got != 0 {
		t.Fatalf("maxTemp on empty FS = %d, want 0", got)
	}
}

func TestDecide(t *testing.T) {
	table := []struct {
		name                string
		temp                int
		paused              bool
		pausedFor, minPause int
		want                decision
	}{
		{"running below high", 80, false, 0, 15, decideNone},
		{"running at high", 88, false, 0, 15, decideStop},
		{"running above high", 91, false, 0, 15, decideStop},
		{"paused but still hot", 84, true, 60, 15, decideNone},
		{"paused, cooled, min elapsed", 78, true, 15, 15, decideCont},
		{"paused, cooled, min not elapsed", 78, true, 14, 15, decideNone},
		{"paused, cooled below low, no wait", 70, true, 0, 15, decideNone},
	}
	for _, c := range table {
		if got := decide(c.temp, c.paused, c.pausedFor, c.minPause, defaultHigh, defaultLow); got != c.want {
			t.Errorf("%s: decide(%d, %v, %d, %d) = %d, want %d",
				c.name, c.temp, c.paused, c.pausedFor, c.minPause, got, c.want)
		}
	}
}

func TestBuildRyzenadjArgs(t *testing.T) {
	got := buildRyzenadjArgs(85, 88000, 76000, 105000)
	want := []string{
		"--tctl-temp=85",
		"--stapm-limit=88000",
		"--slow-limit=76000",
		"--fast-limit=105000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildRyzenadjArgs = %v, want %v", got, want)
	}
}
