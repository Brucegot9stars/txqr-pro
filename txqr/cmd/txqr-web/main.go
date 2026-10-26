// Command txqr-web is the standalone form of the QRT web sender. It exposes
// exactly the same service the unified desktop app (cmd/qrt-pro) embeds, with
// the console-free behaviour required when double-clicked: logs go to a file
// beside the executable instead of a stderr nobody can see.
package main

import (
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/divan/txqr/internal/sender"
)

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	flag.Parse()

	if f := openLog(); f != nil {
		log.SetOutput(f)
		defer f.Close()
	} else {
		log.SetOutput(io.Discard)
	}

	srv := sender.New()
	if err := srv.Start(*addr); err != nil {
		log.Printf("start failed: %v", err)
		os.Exit(1)
	}
	log.Printf("TXQR web sender listening on %s", srv.Addr())

	// Shut down cleanly on Ctrl-C so the listener is released promptly.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
	case <-srv.Done():
	}
	if err := srv.Close(); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// openLog redirects logging to a file next to the executable, because a GUI
// subsystem build (or a double-clicked launcher) has no console to write to.
func openLog() *os.File {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(exe), "txqr-web.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}
