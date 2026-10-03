package cli

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/edkadigital/cli/internal/ui"
)

// requestLog prints a line to stderr for each request the CLI sends: the
// method and URL, then the status and Edka's request ID, or why the request
// failed, and the time until the answer started. It prints no header and no
// body, so no token and no secret.
type requestLog struct {
	next http.RoundTripper
	out  io.Writer
	// Requests that run together print one line at a time.
	printing sync.Mutex
}

func (l *requestLog) RoundTrip(request *http.Request) (*http.Response, error) {
	started := time.Now()
	response, err := l.next.RoundTrip(request)
	line := fmt.Sprintf("debug: %s %s", request.Method, request.URL.Redacted())
	took := time.Since(started).Milliseconds()
	if err != nil {
		line += fmt.Sprintf(" failed %dms: %v", took, err)
	} else {
		line += fmt.Sprintf(" %d %dms", response.StatusCode, took)
		if id := response.Header.Get("X-Request-ID"); id != "" {
			line += " (request " + id + ")"
		}
	}
	l.printing.Lock()
	defer l.printing.Unlock()
	fmt.Fprintln(l.out, ui.Clean(line))
	return response, err
}

// logRequests makes every request of this command print a requestLog line.
// The lines replace the request animation, which would draw over them.
func (a *App) logRequests() {
	next := a.HTTP.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	a.HTTP.Transport = &requestLog{next: next, out: a.Err}
	a.polling = true
}
