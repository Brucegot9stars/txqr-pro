package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// distDir locates the repository's dist/ folder relative to this source file,
// so the test does not depend on a hardcoded absolute path (which broke when
// the checkout moved to a differently-named directory).
func distDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// launcher_test.go lives at <repo>/txqr/cmd/txqr-launcher/
	dir := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "dist"))
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("dist directory not found at %s: %v", dir, err)
	}
	return dir
}

// requireSender resolves the sender exe inside dist and skips the test when
// it has not been built yet (the exes are release artifacts, not tracked).
func requireSender(t *testing.T, dir string) string {
	t.Helper()
	exe := (&launcher{dir: dir}).senderExe()
	if _, err := os.Stat(exe); err == nil {
		return exe
	}
	// The launcher resolves whichever spelling is present in dist; if none of
	// the known names exist the test has nothing to drive.
	for _, name := range senderCandidates {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return filepath.Join(dir, name)
		}
	}
	t.Skipf("no sender exe found in %s (looked for %v)", dir, senderCandidates)
	return ""}

// TestLauncherProcessInfo starts the real sender exe from dist, verifies the
// process is detected with its PID and listen port, then stops it.
func TestLauncherProcessInfo(t *testing.T) {
	dir := distDir(t)
	a := &launcher{dir: dir}
	exe := requireSender(t, dir)
	prefix := procPrefix(exe)

	stopProcess(prefix)
	time.Sleep(300*time.Millisecond)
	defer stopProcess(prefix)

	startProcess(0, exe, a.dir)
	time.Sleep(1500 * time.Millisecond) // let it bind :9000

	running, pid := exeRunning(prefix)
	if !running {
		t.Fatal("sender not detected after start")
	}
	if pid == 0 {
		t.Fatal("no pid")
	}
	ports := listenPorts(pid)
	found := false
	for _, p := range ports {
		if p == 9000 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected listen port 9000, got %v", ports)
	}

	ver, bt := exeMeta(exe)
	if ver != "" && !strings.HasPrefix(ver, "v1.3.0") {
		t.Fatalf("version %q", ver)
	}
	if bt == "" {
		t.Fatal("no build time")
	}
	t.Logf("sender pid=%d ports=%v version=%s build=%s", pid, ports, ver, bt)
}

// TestListenPortsReportsIPv6 tests the AF_INET6 path against the real sender
// too (Go's ":9000" may bind IPv6), with retries for bind timing.
func TestListenPortsReportsIPv6(t *testing.T) {
	dir := distDir(t)
	a := &launcher{dir: dir}
	exe := requireSender(t, dir)
	prefix := procPrefix(exe)
	stopProcess(prefix)
	time.Sleep(300 * time.Millisecond)
	defer stopProcess(prefix)

	startProcess(0, exe, a.dir)
	var got []int
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		running, pid := exeRunning(prefix)
		if !running {
			continue
		}
		got = listenPorts(pid)
		for _, p := range got {
			if p == 9000 {
				t.Logf("found :9000 (pid %d) v4/v6 merged ports=%v", pid, got)
				return
			}
		}
	}
	t.Fatalf("never saw :9000, last ports=%v", got)
}
