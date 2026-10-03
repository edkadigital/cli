package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/term"
)

// ReadSecret reads a line from a terminal without echoing it. The terminal
// stops echoing before the prompt is shown, so nothing typed or pasted ahead
// of the read appears. Ctrl+C and an interrupted read restore the terminal
// and return context.Canceled.
func ReadSecret(ctx context.Context, prompt string, in io.Reader, out io.Writer) (string, error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("hidden input needs a terminal")
	}
	fd := int(f.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	fmt.Fprint(out, prompt)
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := readHiddenLine(f)
		done <- result{value, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		// The read stays blocked until the process exits, which follows.
		r.err = ctx.Err()
	}
	_ = term.Restore(fd, state)
	fmt.Fprintln(out)
	return r.value, r.err
}

// readHiddenLine reads keys from a raw terminal until Enter. Backspace deletes
// the last character, Ctrl+C cancels, and Ctrl+D on an empty line ends input.
func readHiddenLine(r io.Reader) (string, error) {
	var line []byte
	key := make([]byte, 1)
	for {
		n, err := r.Read(key)
		if n == 0 {
			if err == nil {
				continue
			}
			if len(line) > 0 && errors.Is(err, io.EOF) {
				return string(line), nil
			}
			return "", err
		}
		switch key[0] {
		case '\r', '\n':
			return string(line), nil
		case 3: // Ctrl+C
			return "", context.Canceled
		case 4: // Ctrl+D
			if len(line) == 0 {
				return "", io.EOF
			}
		case 8, 127: // Backspace
			if len(line) > 0 {
				_, size := utf8.DecodeLastRune(line)
				line = line[:len(line)-size]
			}
		default:
			line = append(line, key[0])
		}
	}
}
