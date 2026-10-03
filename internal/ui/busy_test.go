package ui

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBusyDrawsLabelBeforeFirstTick(t *testing.T) {
	var out bytes.Buffer
	if err := spin(context.Background(), &out, "Connecting to Edka…", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	drawn, cleared, _ := strings.Cut(out.String(), "\r\033[2K")
	if !strings.Contains(drawn, "Connecting to Edka…") || cleared != "" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestBusyAnimationContinuesAcrossRequests(t *testing.T) {
	start := time.UnixMilli(0)
	for i := range 2 * len(spinnerFrames) {
		now := start.Add(time.Duration(i) * spinnerInterval)
		next := slices.Index(spinnerFrames, spinnerFrame(now.Add(spinnerInterval)))
		if want := (slices.Index(spinnerFrames, spinnerFrame(now)) + 1) % len(spinnerFrames); next != want {
			t.Fatalf("frame after %d = %d, want %d", i, next, want)
		}
	}
}
