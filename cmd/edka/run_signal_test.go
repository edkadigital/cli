//go:build darwin || linux

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestSignalHelper is the command `edka run` starts in these tests. It counts
// the signals it gets while it takes a moment to clean up, then exits with 7.
func TestSignalHelper(t *testing.T) {
	if os.Getenv("EDKA_SIGNAL_HELPER") != "1" {
		return
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	fmt.Println("ready")
	first := <-signals
	count := 1
	for cleanup := time.After(300 * time.Millisecond); cleanup != nil; {
		select {
		case <-signals:
			count++
		case <-cleanup:
			cleanup = nil
		}
	}
	fmt.Printf("cleaned up after %d %s\n", count, first)
	os.Exit(7)
}

// signalRun is `edka run` starting TestSignalHelper, in a process group of
// their own.
func signalRun(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelper$", "--", "run", "--", os.Args[0], "-test.run=^TestSignalHelper$")
	cmd.Env = append(os.Environ(), "EDKA_MAIN_HELPER=1", "EDKA_SIGNAL_HELPER=1", "EDKA_CONFIG_DIR="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}

// startWithDeadline starts cmd and kills its process group when the test ends,
// or after 30 seconds. A command that never gets its signal would otherwise
// keep the test reading its output forever. It reports whether time ran out.
func startWithDeadline(t *testing.T, cmd *exec.Cmd) *atomic.Bool {
	t.Helper()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	kill := func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	var expired atomic.Bool
	timer := time.AfterFunc(30*time.Second, func() {
		expired.Store(true)
		kill()
	})
	t.Cleanup(func() {
		timer.Stop()
		// Once Wait has returned, the process group's ID may belong to another.
		if cmd.ProcessState == nil {
			kill()
		}
	})
	return &expired
}

// A signal sent to edka alone, as a supervisor or CI runner sends it, reaches
// the command once. edka waits for the command's cleanup and exits with its code.
func TestRunPassesSignalsOnAndWaits(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			cmd := signalRun(t)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var errOut bytes.Buffer
			cmd.Stderr = &errOut
			expired := startWithDeadline(t, cmd)
			reader := bufio.NewReader(stdout)
			if line, err := reader.ReadString('\n'); line != "ready\n" {
				t.Fatalf("the command did not start: %q %v %s", line, err, errOut.String())
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			rest, err := io.ReadAll(reader)
			cmd.Wait()
			if err != nil || expired.Load() {
				t.Fatalf("edka run did not exit: read %q, error %v, stderr %q", rest, err, errOut.String())
			}
			if want := fmt.Sprintf("cleaned up after 1 %s\n", sig); string(rest) != want {
				t.Fatalf("the command printed %q, want %q", rest, want)
			}
			if code := cmd.ProcessState.ExitCode(); code != 7 || errOut.Len() != 0 {
				t.Fatalf("code=%d stderr=%q", code, errOut.String())
			}
		})
	}
}

// A terminal sends Ctrl+C to edka and the command together, so edka must not
// send the command a second interrupt.
func TestRunLeavesCtrlCToTheTerminal(t *testing.T) {
	master, tty, err := openPTY()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	defer master.Close()
	cmd := signalRun(t)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	// The pty is the controlling terminal, with edka and the command in its
	// foreground process group, as under a shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	expired := startWithDeadline(t, cmd)
	tty.Close()
	var mu sync.Mutex
	var output bytes.Buffer
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := master.Read(chunk)
			mu.Lock()
			output.Write(chunk[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	printed := func(text string) bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(output.String(), text)
	}
	waitFor := func(text string) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !printed(text); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("no %q in %q", text, output.String())
			}
		}
	}
	waitFor("ready")
	if _, err := master.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitFor("cleaned up after 1 interrupt")
	cmd.Wait()
	if code := cmd.ProcessState.ExitCode(); code != 7 || expired.Load() {
		t.Fatalf("code=%d expired=%v", code, expired.Load())
	}
}
