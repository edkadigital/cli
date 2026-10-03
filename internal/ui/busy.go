package ui

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Busy animates only stderr terminals; stdout remains available for JSON pipes.
func Busy(ctx context.Context, out io.Writer, label string, fn func() error) error {
	if !IsTerminal(out) {
		return fn()
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		timer := time.NewTicker(90 * time.Millisecond)
		defer timer.Stop()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		for {
			select {
			case <-done:
				fmt.Fprint(out, "\r\033[2K")
				return
			case <-ctx.Done():
				fmt.Fprint(out, "\r\033[2K")
				return
			case <-timer.C:
				fmt.Fprintf(out, "\r%s %s", frames[i%len(frames)], label)
				i++
			}
		}
	}()
	err := fn()
	close(done)
	<-stopped
	return err
}
