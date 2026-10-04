package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// issuer is an OIDC issuer for the tests: its discovery document, its key,
// and the RS256 access tokens it signs.
type issuer struct {
	*httptest.Server
	key *rsa.PrivateKey
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	is := &issuer{key: key}
	mux := http.NewServeMux()
	is.Server = httptest.NewServer(mux)
	t.Cleanup(is.Close)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": is.URL, "jwks_uri": is.URL + "/jwks",
			"authorization_endpoint": is.URL + "/auth", "token_endpoint": is.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "k1",
			"alg": "RS256", "use": "sig", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	})
	return is
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// token signs an access token for the chino audience; extra is raw JSON of
// further claims ("" for none), so a test can send any value.
func (is *issuer) token(t *testing.T, extra string) string {
	t.Helper()
	claims := `{"iss":"` + is.URL + `","sub":"kid-1","aud":"chino","iat":` + strconv.FormatInt(time.Now().Unix(), 10) +
		`,"exp":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	if extra != "" {
		claims += "," + extra
	}
	signed := b64([]byte(`{"alg":"RS256","kid":"k1","typ":"JWT"}`)) + "." + b64([]byte(claims+"}"))
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, is.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + b64(sig)
}

// A verified bearer's max_rating claim caps its viewer at the age it says, on
// the request (MaxRating); a bearer without the claim is not capped; a claim
// that is no whole number of years is the strictest cap, 0, said once.
func TestTheBearersMaxRatingRidesOnTheRequest(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	malformedCap = sync.Once{}

	is := newIssuer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v, err := NewVerifier(ctx, is.URL, "chino")
	if err != nil {
		t.Fatal(err)
	}
	h := v.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		age, capped := MaxRating(r.Context())
		if !capped {
			_, _ = w.Write([]byte("uncapped"))
			return
		}
		_, _ = w.Write([]byte(strconv.Itoa(age)))
	}))
	for extra, want := range map[string]string{
		``:                         "uncapped",
		`"max_rating":12`:          "12",
		`"max_rating":0`:           "0",
		`"max_rating":16.0`:        "16",
		`"max_rating":18`:          "18",
		`"max_rating":12.5`:        "0",
		`"max_rating":-3`:          "0",
		`"max_rating":"12"`:        "0",
		`"max_rating":null`:        "0",
		`"max_rating":true`:        "0",
		`"max_rating":[12]`:        "0",
		`"max_rating":{"age":12}`:  "0",
		`"max_rating":1e300`:       "0",
		`"other":{"max_rating":6}`: "uncapped",
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/movies", nil)
		r.Header.Set("Authorization", "Bearer "+is.token(t, extra))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("claims {%s}: %d %q, want %s", extra, w.Code, w.Body, want)
		}
	}
	if n := strings.Count(logged.String(), "no whole number of years"); n != 1 {
		t.Errorf("malformed claims said %d times, want once:\n%s", n, logged.String())
	}
	if _, capped := MaxRating(context.Background()); capped {
		t.Error("a request without a bearer is capped")
	}
}
