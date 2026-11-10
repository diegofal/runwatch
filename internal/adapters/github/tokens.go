package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// tokenTTL is how long a repo keeps the token that last worked for it.
const tokenTTL = 30 * time.Minute

// GHRunner runs the gh CLI and returns its output.
type GHRunner func(ctx context.Context, args ...string) ([]byte, error)

// Tokens finds a token per repo. With a fixed token (RUNWATCH_GITHUB_TOKEN)
// it always answers that. Otherwise it asks the gh CLI: for each account gh
// is logged in to, it takes that account's token and keeps the first one
// that can read the repo. Tokens stay in memory; they are never written to
// disk or to a log.
type Tokens struct {
	fixed string
	gh    GHRunner
	base  string
	http  *http.Client

	mu    sync.Mutex
	cache map[string]tokenHit
}

type tokenHit struct {
	token string
	at    time.Time
}

// NewTokens returns the token source. fixed may be ""; gh may be nil when
// fixed is set.
func NewTokens(fixed string, gh GHRunner, base string, hc *http.Client) *Tokens {
	if base == "" {
		base = DefaultBase
	}
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return &Tokens{fixed: fixed, gh: gh, base: strings.TrimRight(base, "/"), http: hc, cache: map[string]tokenHit{}}
}

var accountRe = regexp.MustCompile(`(?m)Logged in to github\.com account (\S+)`)

// Token returns a token that can read repo.
func (t *Tokens) Token(ctx context.Context, repo string) (string, error) {
	if t.fixed != "" {
		return t.fixed, nil
	}
	t.mu.Lock()
	hit, ok := t.cache[repo]
	t.mu.Unlock()
	if ok && time.Since(hit.at) < tokenTTL {
		return hit.token, nil
	}
	if t.gh == nil {
		return "", errors.New("no GitHub token: set RUNWATCH_GITHUB_TOKEN or install the gh CLI and run `gh auth login`")
	}
	// gh prints the account list on stderr and exits non-zero when any one
	// account's token is bad, so the output is used either way.
	out, statusErr := t.gh(ctx, "auth", "status", "--hostname", "github.com")
	accounts := accountRe.FindAllStringSubmatch(string(out), -1)
	if len(accounts) == 0 {
		if statusErr != nil {
			return "", fmt.Errorf("no GitHub token: `gh auth status` failed (is gh installed and logged in?): %w", statusErr)
		}
		return "", errors.New("no GitHub token: gh is not logged in to github.com (run `gh auth login`)")
	}
	var tried []string
	for _, m := range accounts {
		raw, err := t.gh(ctx, "auth", "token", "--hostname", "github.com", "--user", m[1])
		token := strings.TrimSpace(string(raw))
		if err != nil || token == "" {
			tried = append(tried, m[1]+" (no token)")
			continue
		}
		if t.canRead(ctx, token, repo) {
			t.mu.Lock()
			t.cache[repo] = tokenHit{token: token, at: time.Now()}
			t.mu.Unlock()
			return token, nil
		}
		tried = append(tried, m[1])
	}
	return "", fmt.Errorf("no gh account can read %s (tried: %s)", repo, strings.Join(tried, ", "))
}

// Forget drops the token remembered for repo, after GitHub refused it.
func (t *Tokens) Forget(repo string) {
	t.mu.Lock()
	delete(t.cache, repo)
	t.mu.Unlock()
}

func (t *Tokens) canRead(ctx context.Context, token, repo string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.base+"/repos/"+repo, http.NoBody)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "runwatch")
	resp, err := t.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close() //nolint:errcheck // only the status matters
	return resp.StatusCode == http.StatusOK
}
