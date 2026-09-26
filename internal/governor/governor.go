// Package governor wires the thermal sensors, the ryzenadj wrapper and the
// watchdog into the user-facing subcommands. It keeps I/O and formatting out
// of main so they can be replaced with fakes in tests.
package governor

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eSlider/ryzenadj/internal/thermal"
)

// GPU describes one DRM card's device attributes.
type GPU struct {
	Card  string
	Level string
	Busy  string
	Sclk  string
}

// Status renders a human-readable snapshot to out. sysfs is the sysfs root
// (DefaultSysfs in production). ryzenadjInfo is the optional raw output of
// `ryzenadj -i`; when empty the limits section says it is unavailable.
func Status(out io.Writer, sysfs, ryzenadjInfo string) error {
	readings, err := thermal.Scan(sysfs)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, "Temperatures:")
	if len(readings) == 0 {
		fmt.Fprintln(out, "  (no sensors found)")
	}
	for _, r := range readings {
		fmt.Fprintf(out, "  %-26s %3d C\n", r.ID(), r.CelsiusRound())
	}

	if hottest, ok := thermal.MaxReading(readings); ok {
		fmt.Fprintf(out, "\nHottest: %d C (%s)\n", hottest.CelsiusRound(), hottest.ID())
	}
	if t, ok := thermal.MaxCelsius(readings, thermal.SensorMatchers...); ok {
		fmt.Fprintf(out, "Governor sensor max: %d C (watched: %s)\n",
			t, strings.Join(thermal.SensorMatchers, ", "))
	}

	gpus := GPUs(sysfs)
	fmt.Fprintln(out, "\nGPU:")
	if len(gpus) == 0 {
		fmt.Fprintln(out, "  (no DRM cards found)")
	}
	for _, g := range gpus {
		fmt.Fprintf(out, "  %s\n", g.Card)
		fmt.Fprintf(out, "    power_dpm_force_performance_level: %s\n", orDash(g.Level))
		fmt.Fprintf(out, "    gpu_busy_percent:                  %s\n", orDash(strings.TrimSpace(g.Busy)))
		fmt.Fprintf(out, "    pp_dpm_sclk (current):             %s\n", orDash(g.Sclk))
	}

	fmt.Fprintln(out, "\nryzenadj limits:")
	if strings.TrimSpace(ryzenadjInfo) == "" {
		fmt.Fprintln(out, "  (unavailable: run as root with ryzenadj installed)")
	} else {
		for _, line := range strings.Split(strings.TrimRight(ryzenadjInfo, "\n"), "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
	return nil
}

// GPUs reads the per-card GPU attributes under sysfs. Connector entries such
// as card1-DP-1 are skipped: only numeric cardN devices have these attributes.
func GPUs(sysfs string) []GPU {
	devices, _ := filepath.Glob(filepath.Join(sysfs, "class/drm/card*/device"))
	sort.Strings(devices)
	var out []GPU
	for _, dev := range devices {
		card := filepath.Base(filepath.Dir(dev))
		if !isCardName(card) {
			continue
		}
		out = append(out, GPU{
			Card:  card,
			Level: readTrim(filepath.Join(dev, "power_dpm_force_performance_level")),
			Busy:  readTrim(filepath.Join(dev, "gpu_busy_percent")),
			Sclk:  currentSclk(filepath.Join(dev, "pp_dpm_sclk")),
		})
	}
	return out
}

// isCardName reports whether name is "card" followed by one or more digits.
func isCardName(name string) bool {
	if len(name) <= len("card") || name[:4] != "card" {
		return false
	}
	for _, r := range name[4:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// currentSclk returns the clock state marked with '*' in pp_dpm_sclk.
func currentSclk(path string) string {
	for _, line := range strings.Split(read(path), "\n") {
		if strings.Contains(line, "*") {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "*"))
		}
	}
	return ""
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readTrim(path string) string { return strings.TrimSpace(read(path)) }

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return strings.TrimSpace(s)
}
