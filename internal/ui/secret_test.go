package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReadHiddenLine(t *testing.T) {
	for _, tc := range []struct {
		keys, want string
		err        error
	}{
		{"s3cret\r", "s3cret", nil},
		{"s3cret\n", "s3cret", nil},
		{"abx\x7fc\r", "abc", nil},
		{"ab\x08\x08\x08c\r", "c", nil},
		{"pässwö\x7f\r", "pässw", nil},
		{"abc\x03", "", context.Canceled},
		{"\x04", "", io.EOF},
		{"ab\x04c\r", "abc", nil},
		{"partial", "partial", nil},
		{"", "", io.EOF},
	} {
		got, err := readHiddenLine(strings.NewReader(tc.keys))
		if got != tc.want || !errors.Is(err, tc.err) && err != tc.err {
			t.Errorf("%q: got %q, %v; want %q, %v", tc.keys, got, err, tc.want, tc.err)
		}
	}
}
