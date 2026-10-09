// Package api provides origin-bound, cancellable API requests.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const MaxBody = 32 << 20

// FieldError is one invalid field of a request, named by its path.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

type Error struct {
	Status  int
	Message string
	// Reason is the short label in the body's "error" field, such as
	// "Cluster limit reached", and Code its "code", when they are strings.
	Reason string
	Code   string
	// Details is the body's "details" when it is text, and Fields its "fields".
	// Both are cut to a bounded size.
	Details string
	Fields  []FieldError
	// Data is the body's "data", for a command that reads what a refused request
	// answered, such as the findings of a package. Error does not print it.
	Data      json.RawMessage
	RequestID string
}

const (
	maxErrorDetails = 500
	maxErrorFields  = 50
	maxErrorField   = 200
)

// clip cuts s to n characters. It reads no further than that, since an error
// body may be as large as MaxBody.
func clip(s string, n int) string {
	count, cut := 0, 0
	for i := range s {
		if count == n-1 {
			cut = i
		}
		if count == n {
			return s[:cut] + "…"
		}
		count++
	}
	return s
}

// readFields reads a body's "fields": a list of {field, message}, or an object
// of messages by field, which the secret store and synced secret routes send.
// Fields of an object come in name order.
func readFields(value any) []FieldError {
	var fields []FieldError
	add := func(name, text string) {
		if name != "" && len(fields) < maxErrorFields {
			fields = append(fields, FieldError{Field: clip(name, maxErrorField), Message: clip(text, maxErrorField)})
		}
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			field, _ := item.(map[string]any)
			name, _ := field["field"].(string)
			text, _ := field["message"].(string)
			add(name, text)
		}
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(v)) {
			if text, ok := v[name].(string); ok {
				add(name, text)
			}
		}
	}
	return fields
}

func (e *Error) Error() string {
	hint := ""
	switch e.Status {
	case 401:
		hint = "\nRun `edka login` to sign in again."
	case 403:
		hint = "\nCheck your organization role and granted CLI permissions. Sensitive actions may require the console."
	case 428:
		hint = "\nOpen this action in the Edka console to verify your identity."
	case 429:
		hint = "\nToo many requests. Wait before trying again."
	}
	if e.RequestID != "" {
		hint += "\nRequest ID: " + e.RequestID
	}
	detail := ""
	if e.Details != "" && !strings.Contains(e.Message, e.Details) {
		detail = "\n" + e.Details
	}
	for _, field := range e.Fields {
		detail += "\n  " + field.Field + ": " + field.Message
	}
	return fmt.Sprintf("%s (HTTP %d)%s%s", e.Message, e.Status, detail, hint)
}

// Transient reports whether a request failed in a way that may pass on its
// own: Edka could not be reached, or it answered that it is busy or failing.
func Transient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var apiError *Error
	if errors.As(err, &apiError) {
		// 5xx includes the 52x statuses a proxy answers with while Edka is down.
		return apiError.Status == 408 || apiError.Status == 429 || apiError.Status >= 500
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

type Response struct {
	Body   []byte
	Status int
	Header http.Header
}
type Client struct {
	BaseURL      string
	Token        string
	Organization string
	Version      string
	HTTP         *http.Client
}

func NormalizeBase(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("API URL must be an origin such as https://api.edka.io")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("API URL must use HTTPS; HTTP is allowed only for loopback development")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
func SameOrigin(base, endpoint string) error {
	b, err := url.Parse(base)
	if err != nil {
		return err
	}
	e, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if e.Scheme != b.Scheme || !strings.EqualFold(e.Host, b.Host) || e.User != nil || e.Fragment != "" {
		return errors.New("server advertised an endpoint outside the configured API origin")
	}
	return nil
}
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
func ReadBody(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBody {
		return nil, errors.New("response exceeds 32 MiB; narrow your query")
	}
	return data, nil
}
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body []byte) (*Response, error) {
	if !strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "//") || strings.Contains(path, "#") {
		return nil, errors.New("API path must start with /api/ and contain no fragment")
	}
	u, err := url.Parse(c.BaseURL + path)
	if err != nil {
		return nil, err
	}
	if err := SameOrigin(c.BaseURL, u.String()); err != nil {
		return nil, err
	}
	// A dot segment, percent-encoded or not, would lead out of /api/ on the server.
	for _, segment := range strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == "." || segment == ".." {
			return nil, errors.New(`API path must not contain "." or ".." segments`)
		}
	}
	values := u.Query()
	for k, v := range query {
		values[k] = v
	}
	u.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "edka-cli/"+c.Version)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if c.Organization != "" {
		req.Header.Set("X-Organization-ID", c.Organization)
	}
	client := c.HTTP
	if client == nil {
		client = HTTPClient(30 * time.Second)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect to Edka: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := ReadBody(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := http.StatusText(resp.StatusCode)
		var reason, code, details string
		var fields []FieldError
		var payload json.RawMessage
		var v map[string]any
		if json.Unmarshal(data, &v) == nil {
			var body struct {
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(data, &body) == nil {
				payload = body.Data
			}
			reason, _ = v["error"].(string)
			code, _ = v["code"].(string)
			details, _ = v["details"].(string)
			fields = readFields(v["fields"])
			for _, k := range []string{"error_description", "message", "error"} {
				if text, ok := v[k].(string); ok && text != "" {
					message = text
					break
				}
			}
			if errorObject, ok := v["error"].(map[string]any); ok {
				if text, ok := errorObject["message"].(string); ok {
					message = text
				}
			}
		}
		return nil, &Error{Status: resp.StatusCode, Message: message, Reason: reason, Code: code, Details: clip(details, maxErrorDetails), Fields: fields, Data: payload, RequestID: resp.Header.Get("X-Request-ID")}
	}
	return &Response{Body: data, Status: resp.StatusCode, Header: resp.Header}, nil
}
func Data(body []byte) (any, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if m, ok := v.(map[string]any); ok {
		if data, exists := m["data"]; exists {
			return data, nil
		}
	}
	return v, nil
}
