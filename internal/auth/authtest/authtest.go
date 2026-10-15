// Package authtest serves an OIDC issuer for tests: its discovery document
// and signing key, and the access tokens it signs, so a test meets the bearer
// tokens the service verifies in a deployment.
package authtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Issuer is an OIDC issuer at URL, serving its discovery document and its
// signing key (RS256).
type Issuer struct {
	URL string
	key *rsa.PrivateKey
}

const kid = "authtest"

// NewIssuer starts an issuer that serves until t ends.
func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate the issuer's key: %v", err)
	}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	iss := &Issuer{URL: srv.URL, key: key}
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                iss.URL,
			"authorization_endpoint":                iss.URL + "/auth",
			"token_endpoint":                        iss.URL + "/token",
			"jwks_uri":                              iss.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": b64(key.PublicKey.N.Bytes()),
			"e": b64(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	})
	return iss
}

// Token signs claims as an access token of the issuer; iss, iat and exp (an
// hour from now) are added unless claims give them.
func (i *Issuer) Token(t testing.TB, claims map[string]any) string {
	t.Helper()
	c := map[string]any{"iss": i.URL, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range claims {
		c[k] = v
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	input := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign a token: %v", err)
	}
	return input + "." + b64(sig)
}

// Callers whose tokens the platform's realm issues, by what they are, as
// katalog-manager's tests know them. Each is for the audience chino, which
// the bundled realm makes its tokens for; extra adds or replaces claims.

// Person is a person signed in through the web client, without a rating cap.
func (i *Issuer) Person(t testing.TB, extra map[string]any) string {
	t.Helper()
	return i.Token(t, with(map[string]any{"sub": "viewer-1", "azp": "zaentrum-web", "aud": "chino",
		"preferred_username": "viewer", "realm_access": map[string]any{"roles": []string{"zaentrum-user", "offline_access"}}},
		extra))
}

// Addon is an addon's service account: a client-credentials token of the
// addon's client, with the addon role.
func (i *Issuer) Addon(t testing.TB, extra map[string]any) string {
	t.Helper()
	return i.Token(t, with(map[string]any{"sub": "addon-1", "azp": "example-addon", "aud": "chino",
		"preferred_username": "service-account-example-addon",
		"realm_access":       map[string]any{"roles": []string{"zaentrum-addon"}}}, extra))
}

func with(claims, extra map[string]any) map[string]any {
	for k, v := range extra {
		claims[k] = v
	}
	return claims
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
