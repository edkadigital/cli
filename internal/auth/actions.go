package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/edkadigital/cli/internal/api"
)

// A GitHub Actions job with `permissions: id-token: write` gets these two
// variables, which request an OIDC token for an audience of its choice.
const (
	ActionsRequestURL   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	ActionsRequestToken = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
)

// renewBefore is how long before it expires a trust's access token is
// exchanged again, so no request starts with one about to run out.
const renewBefore = 2 * time.Minute

var trustID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Trust signs in from a GitHub Actions job: it asks the runner for an OIDC
// token for Edka's audience, and Edka exchanges it for an access token of the
// trust. It exchanges a new token before the last one expires, so a long
// --wait keeps working. Nothing is saved.
type Trust struct {
	ID      string
	BaseURL string
	Version string
	HTTP    *http.Client
	// Getenv reads the runner's variables; nil reads the process environment.
	Getenv func(string) string
	// Now is the clock; nil is time.Now.
	Now func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewTrust checks that the trust ID is one and that the job can request an
// OIDC token, so a misconfigured workflow fails before its first request.
func NewTrust(id, base, version string, client *http.Client, getenv func(string) string) (*Trust, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if !trustID.MatchString(id) {
		return nil, fmt.Errorf("EDKA_TRUST must be the ID of a trust, such as 7d1e4c62-3c4b-4b8e-9b1f-0a4f2c1d9e01")
	}
	if getenv(ActionsRequestURL) == "" || getenv(ActionsRequestToken) == "" {
		return nil, fmt.Errorf("signing in with a trust needs a GitHub Actions job with `permissions: id-token: write`; %s is not set", ActionsRequestURL)
	}
	return &Trust{ID: id, BaseURL: base, Version: version, HTTP: client, Getenv: getenv}, nil
}

// Audience is the `aud` Edka requires of a GitHub token: the API's origin.
func Audience(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", errors.New("API URL must be an origin such as https://api.edka.io")
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

func (t *Trust) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Token returns an access token of the trust, exchanging a new GitHub token
// when there is none or the last one expires within renewBefore.
func (t *Trust) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && t.expiresAt.Sub(t.now()) > renewBefore {
		return t.token, nil
	}
	audience, err := Audience(t.BaseURL)
	if err != nil {
		return "", err
	}
	idToken, err := t.idToken(ctx, audience)
	if err != nil {
		return "", err
	}
	token, lifetime, err := t.exchange(ctx, idToken)
	if err != nil {
		return "", err
	}
	t.token, t.expiresAt = token, t.now().Add(lifetime)
	return t.token, nil
}

// idToken asks the runner for an OIDC token for the audience. The request
// token goes only to the runner's own URL, over HTTPS, or HTTP on loopback
// for a simulated runner, and never through a redirect.
func (t *Trust) idToken(ctx context.Context, audience string) (string, error) {
	u, err := url.Parse(t.Getenv(ActionsRequestURL))
	if err != nil || u.Host == "" || u.User != nil {
		return "", fmt.Errorf("%s is not a URL", ActionsRequestURL)
	}
	ip := net.ParseIP(u.Hostname())
	loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", fmt.Errorf("%s must use HTTPS", ActionsRequestURL)
	}
	query := u.Query()
	query.Set("audience", audience)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+t.Getenv(ActionsRequestToken))
	req.Header.Set("Accept", "application/json; api-version=2.0")
	req.Header.Set("User-Agent", "edka-cli/"+t.Version)
	client := t.HTTP
	if client == nil {
		client = api.HTTPClient(30 * time.Second)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request a GitHub OIDC token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := api.ReadBody(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub refused the OIDC token request (HTTP %d); check that the job has `permissions: id-token: write`", resp.StatusCode)
	}
	var answer struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.Value == "" {
		return "", errors.New("GitHub answered the OIDC token request without a token")
	}
	return answer.Value, nil
}

// exchange trades the GitHub token for an access token of the trust and
// returns how long the access token lasts.
func (t *Trust) exchange(ctx context.Context, idToken string) (string, time.Duration, error) {
	body, err := json.Marshal(map[string]string{"trust": t.ID, "token": idToken})
	if err != nil {
		return "", 0, err
	}
	client := api.Client{BaseURL: t.BaseURL, Version: t.Version, HTTP: t.HTTP, Trust: t.ID}
	response, err := client.Do(ctx, http.MethodPost, "/api/cli/github-actions/token", nil, body)
	if err != nil {
		var refused *api.Error
		if errors.As(err, &refused) && (refused.Status == http.StatusUnauthorized || refused.Status == http.StatusForbidden) {
			refused.Hint = "Edka checks the GitHub token, then compares the job's repository, ref, environment and workflow file with the trust. See the trust in the organization settings of the Edka console."
		}
		return "", 0, fmt.Errorf("sign in with trust %s: %w", t.ID, err)
	}
	var answer struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if json.Unmarshal(response.Body, &answer) != nil || answer.AccessToken == "" || answer.ExpiresIn <= 0 || !strings.EqualFold(answer.TokenType, "bearer") {
		return "", 0, errors.New("invalid token exchange response")
	}
	return answer.AccessToken, time.Duration(answer.ExpiresIn) * time.Second, nil
}
