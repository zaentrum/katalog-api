package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Verifier wraps an OIDC verifier. Its inner *oidc.IDTokenVerifier stays nil
// until OIDC discovery against the issuer succeeds, at which point Middleware
// starts validating bearer tokens; until then Middleware fails closed (rejects
// protected requests) so /healthz and /metrics still function.
//
// audiences is the accept-list checked against the token's aud claim — the
// underlying go-oidc verifier runs in SkipClientIDCheck mode so a token
// minted for chino-web-beta or tv-web-beta also passes through the same
// katalog-api instance.
//
// OIDC discovery is resolved LAZILY: the issuer's discovery document is fetched
// once at construction (bounded), and on failure the constructor does NOT error
// — it returns a verifier that fails closed and keeps retrying discovery in the
// background until the issuer is reachable. This is deliberate: in the demo all
// pods (including the bundled Keycloak) roll simultaneously, so at boot the
// issuer is often not yet up. An eager, fatal init left the service running
// with a nil verifier that rejected EVERY authenticated request forever (until
// a manual restart), which degraded chino-api to sample stub data. Failing
// closed + retrying keeps the service booting and self-heals the moment the
// issuer answers.
type Verifier struct {
	issuer    string
	cfg       *oidc.Config
	audiences []string

	mu sync.RWMutex
	v  *oidc.IDTokenVerifier // nil until discovery succeeds
}

// NewVerifier sets up an OIDC verifier against the issuer's discovery
// document. audiences is comma-separated. An empty issuer or empty audiences
// are real configuration errors and are still returned. An issuer that is
// merely unreachable at construction is NOT an error: the returned verifier
// fails closed and retries discovery in the background until it succeeds.
func NewVerifier(ctx context.Context, issuer, audiences string) (*Verifier, error) {
	if issuer == "" {
		return nil, errors.New("oidc issuer empty")
	}
	auds := splitAndTrim(audiences)
	if len(auds) == 0 {
		return nil, errors.New("oidc audiences empty (set KATALOG_API_OIDC_AUDIENCE to a comma-separated list)")
	}
	v := &Verifier{
		issuer:    issuer,
		cfg:       &oidc.Config{SkipClientIDCheck: true},
		audiences: auds,
	}

	// Try once synchronously (bounded) so the common case — issuer already up —
	// is ready the instant the server starts serving. On failure, don't block or
	// error: retry in the background with capped backoff.
	if !v.discover(ctx) {
		slog.Warn("oidc issuer not reachable yet; serving with auth fail-closed, retrying discovery in the background", "issuer", issuer)
		go v.retryDiscovery(ctx)
	}
	return v, nil
}

// discover attempts a single bounded OIDC discovery. On success it installs the
// token verifier and returns true. Safe to call concurrently.
func (v *Verifier) discover(ctx context.Context) bool {
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(dctx, v.issuer)
	if err != nil {
		return false
	}
	tv := provider.Verifier(v.cfg)
	v.mu.Lock()
	v.v = tv
	v.mu.Unlock()
	return true
}

// retryDiscovery re-attempts discovery with capped exponential backoff until it
// succeeds or ctx is cancelled (server shutdown). Because the verifier fails
// closed while discovery is pending, a persisting failure (e.g. a misconfigured
// issuer that will never resolve) is a silent auth outage — every bearer
// request is rejected. So failures are logged (throttled) to keep that state
// visible, not just the single line emitted at startup.
func (v *Verifier) retryDiscovery(ctx context.Context) {
	backoff := 2 * time.Second
	const maxBackoff = 30 * time.Second
	attempts := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		attempts++
		if v.discover(ctx) {
			slog.Info("oidc discovery succeeded; bearer authentication active", "issuer", v.issuer, "attempts", attempts)
			return
		}
		// Surface a persisting outage: the first few attempts, then once per
		// capped-backoff interval so a permanent misconfig stays visible in the
		// logs without flooding them.
		if attempts <= 3 || backoff >= maxBackoff {
			slog.Warn("oidc discovery still failing; bearer auth remains fail-closed, retrying", "issuer", v.issuer, "attempt", attempts)
		}
		if backoff < maxBackoff {
			if backoff *= 2; backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// tokenVerifier returns the installed verifier, or nil if discovery has not yet
// completed.
func (v *Verifier) tokenVerifier() *oidc.IDTokenVerifier {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.v
}

// Middleware enforces a valid Bearer token on protected paths. /healthz and
// /metrics always bypass.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		tv := v.tokenVerifier()
		if v == nil || tv == nil {
			// OIDC discovery not ready yet (or verifier not configured) — fail
			// closed. The background retry self-heals this once the issuer answers.
			slog.Warn("oidc not ready; rejecting request", "path", p)
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
			return
		}
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		tok, err := tv.Verify(r.Context(), raw)
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		// Manual audience check: SkipClientIDCheck=true means the verifier
		// didn't enforce aud — so we do it ourselves against the allowlist.
		var claims struct {
			Aud audClaim `json:"aud"`
		}
		if err := tok.Claims(&claims); err != nil {
			http.Error(w, "claims unreadable", http.StatusUnauthorized)
			return
		}
		matched := false
		for _, want := range v.audiences {
			if slices.Contains([]string(claims.Aud), want) {
				matched = true
				break
			}
		}
		if !matched {
			slog.Warn("audience not in allowlist", "got", []string(claims.Aud), "want_any_of", v.audiences)
			http.Error(w, "audience not permitted", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// audClaim handles both string and []string forms of the OIDC aud claim.
type audClaim []string

func (a *audClaim) UnmarshalJSON(b []byte) error {
	// Try array first
	var arr []string
	if err := json.Unmarshal(b, &arr); err == nil {
		*a = arr
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*a = []string{s}
	return nil
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
