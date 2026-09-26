package thermal

import (
	"os"
	"path/filepath"
	"testing"
)

// buildFakeSysfs creates a minimal sysfs tree for testing.
func buildFakeSysfs(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("class/hwmon/hwmon0/name", "k10temp\n")
	write("class/hwmon/hwmon0/temp1_input", "66000\n")
	write("class/hwmon/hwmon0/temp1_label", "Tctl\n")
	write("class/hwmon/hwmon1/name", "amdgpu\n")
	write("class/hwmon/hwmon1/temp1_input", "65000\n")
	write("class/hwmon/hwmon1/temp1_label", "edge\n")
	write("class/thermal/thermal_zone0/type", "acpitz\n")
	write("class/thermal/thermal_zone0/temp", "64000\n")
	return root
}

func TestScan(t *testing.T) {
	root := buildFakeSysfs(t)
	readings, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(readings) != 3 {
		t.Fatalf("got %d readings, want 3: %+v", len(readings), readings)
	}

	byID := map[string]int{}
	for _, r := range readings {
		byID[r.ID()] = r.CelsiusRound()
	}
	for id, want := range map[string]int{"k10temp/Tctl": 66, "amdgpu/edge": 65, "acpitz": 64} {
		if got := byID[id]; got != want {
			t.Errorf("%s = %d, want %d", id, got, want)
		}
	}
}

func TestScanMissingRoot(t *testing.T) {
	if _, err := Scan(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing sysfs root")
	}
}

func TestMaxCelsius(t *testing.T) {
	root := buildFakeSysfs(t)
	readings, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}

	if v, ok := MaxCelsius(readings, "k10temp", "amdgpu", "acpitz"); !ok || v != 66 {
		t.Errorf("watched max = %d, %v; want 66, true", v, ok)
	}
	if v, ok := MaxCelsius(readings, "amdgpu"); !ok || v != 65 {
		t.Errorf("amdgpu max = %d, %v; want 65, true", v, ok)
	}
	if _, ok := MaxCelsius(readings, "nonexistent"); ok {
		t.Error("expected ok=false for unknown sensor")
	}
	if v, ok := MaxCelsius(readings); !ok || v != 66 {
		t.Errorf("unfiltered max = %d, %v; want 66, true", v, ok)
	}
}

func TestMaxReading(t *testing.T) {
	readings := []Reading{
		{Name: "k10temp", Label: "Tctl", Milli: 66000},
		{Name: "amdgpu", Label: "edge", Milli: 71000},
	}
	best, ok := MaxReading(readings)
	if !ok || best.Name != "amdgpu" {
		t.Fatalf("MaxReading = %+v, %v; want amdgpu", best, ok)
	}
	if _, ok := MaxReading(nil); ok {
		t.Error("expected ok=false for empty slice")
	}
}

func TestParseMilli(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"65000\n", 65000, false},
		{" 65000 ", 65000, false},
		{"0", 0, false},
		{"", 0, true},
		{"abc", 0, true},
		{"65.5", 0, true},
	}
	for _, c := range cases {
		got, err := ParseMilli(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseMilli(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseMilli(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestReadingFormatting(t *testing.T) {
	r := Reading{Name: "k10temp", Label: "Tctl", Milli: 66400}
	if got := r.CelsiusRound(); got != 66 {
		t.Errorf("CelsiusRound = %d, want 66", got)
	}
	if got := r.ID(); got != "k10temp/Tctl" {
		t.Errorf("ID = %q, want k10temp/Tctl", got)
	}
	same := Reading{Name: "acpitz", Label: "acpitz", Milli: 40000}
	if got := same.ID(); got != "acpitz" {
		t.Errorf("ID = %q, want acpitz", got)
	}
}
