package sender

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// canConnect reports whether anything accepts TCP on addr. The point of these
// tests is the OS-level socket, not the Server's own bookkeeping: a server that
// *thinks* it stopped while the port stays open is exactly the bug being
// guarded against.
func canConnect(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// TestStopAfterRestartReleasesPort is the regression test for the reported
// "点停止服务后发送端后台还在跑". Close() used to be wrapped in a single
// sync.Once, so it took effect only the first time in a Server's life: after
// start → stop → start, the next stop did nothing at all. The UI printed
// "发送服务已停止" and the port kept serving.
func TestStopAfterRestartReleasesPort(t *testing.T) {
	srv := startTestServer(t)

	first := srv.Addr()
	if !canConnect(first) {
		t.Fatalf("first Start did not listen on %s", first)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("close #1: %v", err)
	}
	if canConnect(first) {
		t.Fatalf("close #1 left %s open", first)
	}

	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("restart: %v", err)
	}
	second := srv.Addr()
	if second == "" || !canConnect(second) {
		t.Fatalf("restart did not listen (addr=%q)", second)
	}
	if !srv.Stats().Listening {
		t.Fatalf("Stats().Listening false while serving %s", second)
	}

	// The click the user reports: stop the service a second time.
	if err := srv.Close(); err != nil {
		t.Fatalf("close #2: %v", err)
	}
	if srv.Stats().Listening {
		t.Errorf("Stats().Listening still true after close #2")
	}
	if canConnect(second) {
		t.Fatalf("BUG: port %s still accepting connections after close #2", second)
	}
	resp, err := http.Get("http://" + second + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatalf("BUG: HTTP %d still served after close #2", resp.StatusCode)
	}
}

// TestRepeatedCyclesKeepWorking hammers the toggle: five start/stop cycles must
// each bind and each release, without the closed-done-channel panic that a
// shared done channel would cause on the second Serve return.
func TestRepeatedCyclesKeepWorking(t *testing.T) {
	srv := startTestServer(t)
	for i := 0; i < 5; i++ {
		if i > 0 {
			if err := srv.Start("127.0.0.1:0"); err != nil {
				t.Fatalf("cycle %d start: %v", i, err)
			}
		}
		addr := srv.Addr()
		if addr == "" || !canConnect(addr) {
			t.Fatalf("cycle %d: not listening on %q", i, addr)
		}
		if err := srv.Close(); err != nil {
			t.Fatalf("cycle %d close: %v", i, err)
		}
		if canConnect(addr) {
			t.Fatalf("cycle %d: %s still open after Close", i, addr)
		}
	}
}

// TestCloseWithOpenBrowserConnection covers the everyday case: the sender page
// is open in a browser (keep-alive connection) when the user hits stop. Stop
// must not wait for that connection and must still free the port.
func TestCloseWithOpenBrowserConnection(t *testing.T) {
	srv := startTestServer(t)
	addr := srv.Addr()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(buf); err != nil {
		t.Fatalf("read response: %v", err)
	}

	start := time.Now()
	if err := srv.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("Close took %v with an idle keep-alive connection open; it must not wait for it", took)
	}
	if canConnect(addr) {
		t.Fatalf("BUG: %s still open after Close", addr)
	}
}

// TestDoneNeverBlocks pins the contract callers rely on: Done() is closed
// before the first Start, during a run, and after Close — so selecting on it
// can never park a goroutine forever on a stopped server.
func TestDoneNeverBlocks(t *testing.T) {
	srv := New()
	select {
	case <-srv.Done():
	default:
		t.Fatal("Done() blocked before Start")
	}

	if err := srv.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-srv.Done():
		t.Fatal("Done() closed while the server is running")
	case <-time.After(100 * time.Millisecond):
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-srv.Done():
	case <-time.After(time.Second):
		t.Fatal("Done() not closed after Close")
	}

	// Close on a never-started server is a no-op, and Done stays closed.
	if err := srv.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
