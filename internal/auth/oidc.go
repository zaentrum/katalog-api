package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Verifier wraps an OIDC verifier. A nil *Verifier means OIDC is not
// configured / unreachable, in which case Middleware short-circuits to
// allow only /healthz and /metrics so scaffold mode still functions.
//
// audiences is the accept-list checked against the token's aud claim — the
// underlying go-oidc verifier runs in SkipClientIDCheck mode so a token
// minted for chino-web-beta or tv-web-beta also passes through the same
// katalog-api instance.
type Verifier struct {
	v         *oidc.IDTokenVerifier
	audiences []string
}

// NewVerifier sets up an OIDC verifier against the issuer's discovery
// document. audiences is comma-separated; if empty, every audience is
// rejected (defense in depth). Returns a nil *Verifier and the underlying
// error if the provider can't be reached.
func NewVerifier(ctx context.Context, issuer, audiences string) (*Verifier, error) {
	if issuer == "" {
		return nil, errors.New("oidc issuer empty")
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	auds := splitAndTrim(audiences)
	if len(auds) == 0 {
		return nil, errors.New("oidc audiences empty (set KATALOG_API_OIDC_AUDIENCE to a comma-separated list)")
	}
	return &Verifier{
		v:         provider.Verifier(&oidc.Config{SkipClientIDCheck: true}),
		audiences: auds,
	}, nil
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
		if v == nil || v.v == nil {
			slog.Warn("oidc not configured; rejecting request", "path", p)
			http.Error(w, "auth unavailable", http.StatusServiceUnavailable)
			return
		}
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		tok, err := v.v.Verify(r.Context(), raw)
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
