// Package thermal reads temperatures from the Linux sysfs thermal/hwmon
// interfaces and computes aggregate values for the governor.
//
// It is deliberately dependency-free (stdlib only) and root-less: reading
// /sys/class/hwmon and /sys/class/thermal requires no privileges.
package thermal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultSysfs is the standard sysfs mount point.
const DefaultSysfs = "/sys"

// Reading is a single temperature sensor sample.
type Reading struct {
	// Kind is "hwmon" or "thermal".
	Kind string
	// Name is the chip name (hwmon "name", e.g. "k10temp", "amdgpu") or the
	// thermal zone type (e.g. "acpitz").
	Name string
	// Label is the optional channel label (e.g. "Tctl", "edge").
	Label string
	// Milli is the raw millidegree Celsius value.
	Milli int
}

// Celsius returns the reading in degrees Celsius.
func (r Reading) Celsius() float64 { return float64(r.Milli) / 1000.0 }

// CelsiusRound returns the reading rounded to the nearest whole degree.
func (r Reading) CelsiusRound() int {
	return int((r.Milli + 500) / 1000)
}

// ID is a stable human-readable identifier such as "k10temp/Tctl" or "acpitz".
func (r Reading) ID() string {
	if r.Label != "" && !strings.EqualFold(r.Label, r.Name) {
		return r.Name + "/" + r.Label
	}
	return r.Name
}

func (r Reading) String() string {
	return fmt.Sprintf("%-24s %3d C", r.ID(), r.CelsiusRound())
}

// Scan walks the sysfs root and returns every readable temperature sensor.
// A missing sysfs root is an error, but unreadable individual files are
// skipped: sensors come and go, and one bad file must not break monitoring.
func Scan(sysfs string) ([]Reading, error) {
	if sysfs == "" {
		sysfs = DefaultSysfs
	}
	info, err := os.Stat(sysfs)
	if err != nil {
		return nil, fmt.Errorf("sysfs root %q: %w", sysfs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sysfs root %q is not a directory", sysfs)
	}

	var out []Reading

	for _, h := range glob(sysfs, "class/hwmon/hwmon*") {
		name := trimRead(filepath.Join(h, "name"))
		for _, t := range glob(h, "temp*_input") {
			v, err := ParseMilli(readFile(t))
			if err != nil {
				continue
			}
			label := trimRead(strings.TrimSuffix(t, "_input") + "_label")
			out = append(out, Reading{Kind: "hwmon", Name: name, Label: label, Milli: v})
		}
	}

	for _, z := range glob(sysfs, "class/thermal/thermal_zone*") {
		v, err := ParseMilli(readFile(filepath.Join(z, "temp")))
		if err != nil {
			continue
		}
		typ := trimRead(filepath.Join(z, "type"))
		if typ == "" {
			typ = filepath.Base(z)
		}
		out = append(out, Reading{Kind: "thermal", Name: typ, Label: typ, Milli: v})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Label < out[j].Label
	})
	return out, nil
}

// Names returns the distinct sensor names present in readings.
func Names(readings []Reading) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range readings {
		if !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, r.Name)
		}
	}
	sort.Strings(out)
	return out
}

// MaxCelsius returns the hottest reading among sensors whose Name is in
// include. If include is empty every reading is considered. It reports
// ok=false when no matching reading was found.
func MaxCelsius(readings []Reading, include ...string) (value int, ok bool) {
	want := map[string]bool{}
	for _, n := range include {
		want[strings.ToLower(n)] = true
	}
	for _, r := range readings {
		if len(want) > 0 && !want[strings.ToLower(r.Name)] {
			continue
		}
		if v := r.CelsiusRound(); !ok || v > value {
			value, ok = v, true
		}
	}
	return value, ok
}

// MaxReading returns the single hottest reading, or ok=false when empty.
func MaxReading(readings []Reading) (Reading, bool) {
	var best Reading
	ok := false
	for _, r := range readings {
		if !ok || r.Milli > best.Milli {
			best, ok = r, true
		}
	}
	return best, ok
}

// ParseMilli parses a sysfs millidegree value such as "65000" or " 65000\n".
// Anything that is not a plain integer is rejected.
func ParseMilli(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty temperature value")
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid temperature value %q: %w", s, err)
	}
	return v, nil
}

// SensorMatchers are the chips the governor considers authoritative when
// deciding whether to throttle. acpitz is included because it is the ACPI
// thermal zone that most closely tracks the reported package temperature.
var SensorMatchers = []string{"k10temp", "amdgpu", "acpitz"}

func glob(root, pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(root, pattern))
	sort.Strings(m)
	return m
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func trimRead(path string) string { return strings.TrimSpace(readFile(path)) }
