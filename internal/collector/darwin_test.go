//go:build darwin

package collector

import (
	"runtime"
	"testing"
)

func TestDarwinShim(t *testing.T) {
	t.Run("hw.machine matches the architecture the binary runs on", func(t *testing.T) {
		got, err := Machine()
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOARCH == "arm64" && got != "arm64" {
			t.Errorf("hw.machine = %q, want arm64", got)
		}
		if got == "" {
			t.Error("hw.machine is empty")
		}
	})
}
