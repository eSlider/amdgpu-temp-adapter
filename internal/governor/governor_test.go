package governor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildFakeSysfs(t *testing.T) string {
	root := t.TempDir()
	writeFile(t, root, "class/hwmon/hwmon0/name", "k10temp\n")
	writeFile(t, root, "class/hwmon/hwmon0/temp1_input", "65000\n")
	writeFile(t, root, "class/hwmon/hwmon0/temp1_label", "Tctl\n")
	writeFile(t, root, "class/drm/card0/device/power_dpm_force_performance_level", "auto\n")
	writeFile(t, root, "class/drm/card0/device/gpu_busy_percent", "97\n")
	writeFile(t, root, "class/drm/card0/device/pp_dpm_sclk", "0: 200Mhz\n1: 1487Mhz *\n2: 2000Mhz\n")
	// A connector entry must be ignored.
	writeFile(t, root, "class/drm/card0-DP-1/device/gpu_busy_percent", "1\n")
	return root
}

func TestGPUs(t *testing.T) {
	root := buildFakeSysfs(t)
	gpus := GPUs(root)
	if len(gpus) != 1 {
		t.Fatalf("got %d GPUs, want 1: %+v", len(gpus), gpus)
	}
	g := gpus[0]
	if g.Card != "card0" {
		t.Errorf("Card = %q, want card0", g.Card)
	}
	if g.Level != "auto" || g.Busy != "97" {
		t.Errorf("Level/Busy = %q/%q, want auto/97", g.Level, g.Busy)
	}
	if !strings.Contains(g.Sclk, "1487Mhz") {
		t.Errorf("Sclk = %q, want the starred 1487Mhz entry", g.Sclk)
	}
}

func TestIsCardName(t *testing.T) {
	for _, name := range []string{"card0", "card1", "card12"} {
		if !isCardName(name) {
			t.Errorf("isCardName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"card", "card0-DP-1", "cardX", "card0eDP"} {
		if isCardName(name) {
			t.Errorf("isCardName(%q) = true, want false", name)
		}
	}
}

func TestStatus(t *testing.T) {
	root := buildFakeSysfs(t)
	var buf bytes.Buffer
	if err := Status(&buf, root, "STAPM LIMIT 30000\nTHM LIMIT CORE 85\n"); err != nil {
		t.Fatalf("Status: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"k10temp/Tctl",
		"65 C",
		"Hottest: 65 C",
		"card0",
		"auto",
		"gpu_busy_percent:                  97",
		"1487Mhz",
		"STAPM LIMIT 30000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Status output missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "card0-DP-1") {
		t.Error("Status output should not include connector card0-DP-1")
	}
}

func TestStatusMissingRoot(t *testing.T) {
	var buf bytes.Buffer
	if err := Status(&buf, filepath.Join(t.TempDir(), "nope"), ""); err == nil {
		t.Fatal("expected error for missing sysfs root")
	}
}
