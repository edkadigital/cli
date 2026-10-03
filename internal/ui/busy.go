package ui

import (
	"context"
	"fmt"
	"io"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 90 * time.Millisecond

// spinnerFrame picks the frame from the clock, so a command that makes one
// request after another shows one animation instead of restarting it.
func spinnerFrame(now time.Time) string {
	return spinnerFrames[now.UnixMilli()/spinnerInterval.Milliseconds()%int64(len(spinnerFrames))]
}

// Busy animates only stderr terminals; stdout remains available for JSON pipes.
func Busy(ctx context.Context, out io.Writer, label string, fn func() error) error {
	if !IsTerminal(out) {
		return fn()
	}
	return spin(ctx, out, label, fn)
}

// spin draws the label at once and redraws it on every tick. Waiting for the
// first tick would blank the line between consecutive requests.
func spin(ctx context.Context, out io.Writer, label string, fn func() error) error {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(spinnerInterval)
		defer ticker.Stop()
		for {
			fmt.Fprintf(out, "\r%s %s", spinnerFrame(time.Now()), label)
			select {
			case <-done:
				fmt.Fprint(out, "\r\033[2K")
				return
			case <-ctx.Done():
				fmt.Fprint(out, "\r\033[2K")
				return
			case <-ticker.C:
			}
		}
	}()
	err := fn()
	close(done)
	<-stopped
	return err
}
