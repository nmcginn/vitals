package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func build(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "vitals")
	if out, err := exec.Command("go", "build", "-o", bin,
		"-ldflags", "-X main.version=v9.9.9 -X main.commit=abc123 -X main.date=2026-01-02",
		".").CombinedOutput(); err != nil {
		t.Fatalf("building the binary failed: %v\n%s", err, out)
	}
	return bin
}

func TestVersionFlag(t *testing.T) {
	bin := build(t)

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

func TestBadFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"an interval under 1s", []string{"-interval", "500ms"}, "-interval 500ms is too short: the minimum is 1s"},
		{"a zero interval", []string{"-interval", "0s"}, "-interval 0s is too short"},
		{"an interval that isn't a duration", []string{"-interval", "fast"}, "-interval"},
		{"a history shorter than the interval", []string{"-history", "1s", "-interval", "2s"}, "-history 1s is shorter than -interval 2s"},
		{"a disk-exclude regex that doesn't compile", []string{"-disk-exclude", "(snap"}, "-disk-exclude \"(snap\" is not a valid regular expression"},
		{"a net-exclude regex that doesn't compile", []string{"-net-exclude", "[a-"}, "-net-exclude \"[a-\" is not a valid regular expression"},
		{"an unknown log level", []string{"-log-level", "loud"}, "-log-level \"loud\" is not one of debug, info, warn, error"},
		{"an unknown flag", []string{"-port", "80"}, "-port"},
		{"a stray argument", []string{"9110"}, "unexpected argument \"9110\""},
	} {
		t.Run(tc.name+" exits 2 and names the flag", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(context.Background(), tc.args, &stdout, &stderr); got != 2 {
				t.Errorf("exit status %d, want 2", got)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr %q lacks %q", stderr.String(), tc.want)
			}
		})
	}

	t.Run("an address that can't be listened on exits 1 and names it", func(t *testing.T) {
		var stderr bytes.Buffer
		if got := run(context.Background(), []string{"-listen", "256.0.0.1:0"}, io.Discard, &stderr); got != 1 {
			t.Errorf("exit status %d, want 1", got)
		}
		if !strings.Contains(stderr.String(), "-listen 256.0.0.1:0") {
			t.Errorf("stderr %q lacks the address", stderr.String())
		}
	})
}

// syncBuffer is written by the server's goroutines while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startup runs the server in-process until it logs that it's listening, then
// stops it, and returns everything it logged.
func startup(t *testing.T, listen string) string {
	t.Helper()
	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	go func() { done <- run(ctx, []string{"-listen", listen}, io.Discard, &logs) }()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(logs.String(), "msg=listening"); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no listening line within 5s:\n%s", logs.String())
		}
	}
	cancel()
	if status := <-done; status != 0 {
		t.Errorf("exit status %d after a clean shutdown:\n%s", status, logs.String())
	}
	return logs.String()
}

func TestExposureWarning(t *testing.T) {
	t.Run("0.0.0.0:0 logs exactly one warn line saying there is no authentication", func(t *testing.T) {
		logs := startup(t, "0.0.0.0:0")
		var warned []string
		for l := range strings.Lines(logs) {
			if strings.Contains(l, "no authentication") {
				warned = append(warned, l)
			}
		}
		if len(warned) != 1 || !strings.Contains(warned[0], "level=WARN") {
			t.Errorf("warn lines %q, want one:\n%s", warned, logs)
		}
	})

	t.Run("127.0.0.1:0 logs no warning", func(t *testing.T) {
		if logs := startup(t, "127.0.0.1:0"); strings.Contains(logs, "no authentication") {
			t.Errorf("warned on loopback:\n%s", logs)
		}
	})

	t.Run("[::1]:0 logs no warning", func(t *testing.T) {
		if ln, err := net.Listen("tcp", "[::1]:0"); err != nil {
			t.Skipf("this machine has no IPv6 loopback (%v); the exposed() table still covers ::1", err)
		} else {
			ln.Close()
		}
		if logs := startup(t, "[::1]:0"); strings.Contains(logs, "no authentication") {
			t.Errorf("warned on loopback:\n%s", logs)
		}
	})

	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1", false}, {"127.0.0.2", false}, {"::1", false},
		{"0.0.0.0", true}, {"::", true}, {"192.168.1.10", true}, {"100.64.0.1", true},
	} {
		t.Run(tc.addr+" counts as exposed: "+map[bool]string{true: "yes", false: "no"}[tc.want], func(t *testing.T) {
			if got := exposed(&net.TCPAddr{IP: net.ParseIP(tc.addr)}); got != tc.want {
				t.Errorf("exposed(%s) = %v", tc.addr, got)
			}
		})
	}
}

func TestBinary(t *testing.T) {
	t.Run("the built binary serves /healthz 200 within two intervals and exits 0 on SIGTERM", func(t *testing.T) {
		cmd := exec.Command(build(t), "-listen", "127.0.0.1:0", "-interval", "1s")
		stderr, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer cmd.Process.Kill()

		lines := make(chan string, 100)
		go func() {
			sc := bufio.NewScanner(stderr)
			for sc.Scan() {
				lines <- sc.Text()
			}
			close(lines)
		}()
		addrRe := regexp.MustCompile(`msg=listening addr=(\S+)`)
		var addr string
		for addr == "" {
			select {
			case l, ok := <-lines:
				if !ok {
					t.Fatal("the binary exited before listening")
				}
				if m := addrRe.FindStringSubmatch(l); m != nil {
					addr = m[1]
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no listening line within 10s")
			}
		}

		listening := time.Now()
		for {
			res, err := http.Get("http://" + addr + "/healthz")
			if err == nil {
				res.Body.Close()
				if res.StatusCode == 200 {
					break
				}
			}
			if time.Since(listening) > 2*time.Second {
				t.Fatalf("/healthz not 200 within two intervals: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}

		cmd.Process.Signal(syscall.SIGTERM)
		// Read stderr to EOF before Wait, which closes the pipe.
		var rest []string
		timeout := time.After(10 * time.Second)
	drain:
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					break drain
				}
				rest = append(rest, l)
			case <-timeout:
				t.Fatal("still running 10s after SIGTERM")
			}
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("exit after SIGTERM: %v\n%s", err, strings.Join(rest, "\n"))
		}
		if !strings.Contains(strings.Join(rest, "\n"), "shutting down") {
			t.Errorf("no shutdown line: %q", rest)
		}
	})
}
