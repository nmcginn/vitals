package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionFlag(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "vitals")
	if out, err := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X main.version=v9.9.9 -X main.commit=abc123 -X main.date=2026-01-02",
		".").CombinedOutput(); err != nil {
		t.Fatalf("building the binary failed: %v\n%s", err, out)
	}

	t.Run("-version prints the injected version, commit, and date and exits 0", func(t *testing.T) {
		out, err := exec.Command(bin, "-version").Output()
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
		for _, want := range []string{"vitals", "v9.9.9", "abc123", "2026-01-02"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("output %q lacks %q", out, want)
			}
		}
	})

	t.Run("an unknown flag exits 2", func(t *testing.T) {
		err := exec.Command(bin, "-nope").Run()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 {
			t.Errorf("err = %v, want exit status 2", err)
		}
	})
}
