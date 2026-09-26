// Package ryzenadj wraps the external FlyGoat/RyzenAdj binary.
//
// This project is NOT RyzenAdj: it does not talk to the SMU itself. It shells
// out to an existing `ryzenadj` executable, which keeps us cgo-free and avoids
// duplicating kernel-module/PCI access logic. Set RYZENADJ to point at a
// non-standard binary path.
package ryzenadj

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Limits is the thermal/power policy applied to the SoC package.
//
// Power values are in milliwatts (mW); TctlTemp is in degrees Celsius.
type Limits struct {
	TctlTemp   int
	StapmLimit int
	SlowLimit  int
	FastLimit  int
}

// DefaultLimits is the policy verified on a Ryzen 7 5800H: it holds the SoC at
// 63-66 C under full iGPU load while leaving the GPU at full speed. See
// docs/thermal-findings.md.
func DefaultLimits() Limits {
	return Limits{
		TctlTemp:   85,
		StapmLimit: 30000,
		SlowLimit:  25000,
		FastLimit:  35000,
	}
}

// Validation ranges. The lower bounds keep the machine usable; the upper
// bounds refuse values that would defeat the purpose or damage silicon.
const (
	MinTctlTemp = 50
	MaxTctlTemp = 105

	MinStapm = 5000
	MaxStapm = 120000
	MinSlow  = 5000
	MaxSlow  = 120000
	MinFast  = 5000
	MaxFast  = 200000
)

// Validate checks every field against its allowed range and the inter-field
// invariants (fast >= slow >= nothing, tctl sane).
func (l Limits) Validate() error {
	if l.TctlTemp < MinTctlTemp || l.TctlTemp > MaxTctlTemp {
		return fmt.Errorf("tctl-temp %d out of range [%d, %d]", l.TctlTemp, MinTctlTemp, MaxTctlTemp)
	}
	if l.StapmLimit < MinStapm || l.StapmLimit > MaxStapm {
		return fmt.Errorf("stapm-limit %d out of range [%d, %d] mW", l.StapmLimit, MinStapm, MaxStapm)
	}
	if l.SlowLimit < MinSlow || l.SlowLimit > MaxSlow {
		return fmt.Errorf("slow-limit %d out of range [%d, %d] mW", l.SlowLimit, MinSlow, MaxSlow)
	}
	if l.FastLimit < MinFast || l.FastLimit > MaxFast {
		return fmt.Errorf("fast-limit %d out of range [%d, %d] mW", l.FastLimit, MinFast, MaxFast)
	}
	if l.FastLimit < l.SlowLimit {
		return fmt.Errorf("fast-limit (%d mW) must be >= slow-limit (%d mW)", l.FastLimit, l.SlowLimit)
	}
	return nil
}

// Args renders the limits as ryzenadj command-line arguments.
func (l Limits) Args() []string {
	return []string{
		fmt.Sprintf("--tctl-temp=%d", l.TctlTemp),
		fmt.Sprintf("--stapm-limit=%d", l.StapmLimit),
		fmt.Sprintf("--slow-limit=%d", l.SlowLimit),
		fmt.Sprintf("--fast-limit=%d", l.FastLimit),
	}
}

// String renders the limits in the canonical one-liner form.
func (l Limits) String() string {
	return strings.Join(l.Args(), " ")
}

// Find locates the ryzenadj binary using explicit override, then PATH, then
// the conventional install location. It returns a clear error when missing.
func Find(override string) (string, error) {
	if override != "" {
		if p, err := exec.LookPath(override); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("ryzenadj not found at %q", override)
	}
	if p, err := exec.LookPath("ryzenadj"); err == nil {
		return p, nil
	}
	const fallback = "/usr/local/bin/ryzenadj"
	if p, err := exec.LookPath(fallback); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("ryzenadj not found in PATH or /usr/local/bin; install FlyGoat/RyzenAdj or set --ryzenadj")
}

// Apply runs `ryzenadj <args>` and streams its combined output to out.
// It is idempotent: applying the same limits twice is harmless.
func Apply(ctx context.Context, bin string, l Limits, out io.Writer) error {
	if err := l.Validate(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin, l.Args()...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ryzenadj apply failed: %w", err)
	}
	return nil
}

// Info runs `ryzenadj -i` and returns its raw output. The output format is an
// upstream implementation detail, so we surface it verbatim rather than
// depending on a brittle parser.
func Info(ctx context.Context, bin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "-i")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("ryzenadj -i failed: %w", err)
	}
	return string(out), nil
}

// ParseInfo extracts the integer value of a named limit from `ryzenadj -i`
// output (for example "THM LIMIT CORE" or "STAPM LIMIT"). It exists so status
// can show a couple of key values without parsing the whole table; unknown
// names return ok=false instead of an error.
func ParseInfo(output, key string) (int, bool) {
	want := strings.ToUpper(strings.TrimSpace(key))
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// Rebuild the leading uppercase name from the fields, stopping at the
		// first numeric token.
		var name []string
		valIdx := -1
		for i, f := range fields {
			if _, err := parseLeadingInt(f); err == nil {
				valIdx = i
				break
			}
			name = append(name, f)
		}
		if valIdx < 0 {
			continue
		}
		got := strings.ToUpper(strings.Join(name, " "))
		if strings.HasPrefix(got, want) {
			if v, err := parseLeadingInt(fields[valIdx]); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// parseLeadingInt parses the leading integer of a token such as "30000" or
// "30.0000" or "85". Trailing units like "(30.0000" are ignored.
func parseLeadingInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && (s[end] == '-' || s[end] == '+' || (s[end] >= '0' && s[end] <= '9')) {
		end++
	}
	if end == 0 {
		return 0, fmt.Errorf("no leading integer in %q", s)
	}
	var n int
	_, err := fmt.Sscanf(s[:end], "%d", &n)
	return n, err
}
