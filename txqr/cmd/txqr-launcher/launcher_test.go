package main

import (
	"strings"
	"testing"
	"time"
)

// TestLauncherProcessInfo starts the real sender exe from dist, verifies the
// process is detected with its PID and listen port, then stops it.
func TestLauncherProcessInfo(t *testing.T) {
	a := &launcher{dir: `D:\Workspace\AiDev\008-qrt-pro\dist`}
	exe := a.senderExe()

	stopProcess("qrt-pro-sender")
	time.Sleep(300*time.Millisecond)
	defer stopProcess("qrt-pro-sender")

	startProcess(0, exe, a.dir)
	time.Sleep(1500 * time.Millisecond) // let it bind :9000

	running, pid := exeRunning("qrt-pro-sender")
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
	if !strings.HasPrefix(ver, "v1.3.0") {
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
	a := &launcher{dir: `D:\Workspace\AiDev\008-qrt-pro\dist`}
	exe := a.senderExe()
	stopProcess("qrt-pro-sender")
	time.Sleep(300 * time.Millisecond)
	defer stopProcess("qrt-pro-sender")

	startProcess(0, exe, a.dir)
	var got []int
	for i := 0; i < 10; i++ {
		time.Sleep(500 * time.Millisecond)
		running, pid := exeRunning("qrt-pro-sender")
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
