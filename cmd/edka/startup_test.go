//go:build darwin || linux

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Commands that show no picker must not query the terminal. The pty here
// never answers, so a startup query for the background color or cursor
// position would stall the command and leave its escape sequence in the output.
func TestStartupDoesNotQueryTerminal(t *testing.T) {
	master, tty, err := openPTY()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer master.Close()
	// The same command without a terminal sets the expected run time, which
	// the race detector raises to about a second on macOS.
	baseline := time.Now()
	if out, _, code := runMain(t, nil, "version"); code != 0 || !strings.Contains(out, "edka dev") {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	expected := time.Since(baseline)
	cmd := exec.Command(os.Args[0], "-test.run=TestMainHelper", "--", "version")
	// Terminal libraries skip their queries under CI and in dumb terminals,
	// screen and tmux, so the child sees a plain interactive terminal.
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "CI=") && !strings.HasPrefix(v, "TERM=") {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "TERM=xterm-256color", "EDKA_MAIN_HELPER=1", "EDKA_CONFIG_DIR="+t.TempDir())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	// The pty becomes the child's controlling terminal with the child in the
	// foreground, as under a shell; background processes are never queried.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	output := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(master)
		output <- data
	}()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	var out []byte
	select {
	case out = <-output:
	case <-time.After(5 * time.Second):
		t.Fatal("pty output did not close")
	}
	if bytes.IndexByte(out, 0x1b) >= 0 || !bytes.Contains(out, []byte("edka dev")) {
		t.Fatalf("output %q", out)
	}
	if elapsed > expected+time.Second {
		t.Fatalf("edka version took %s in a terminal and %s without one", elapsed, expected)
	}
}
