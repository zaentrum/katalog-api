package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewVerifier_UnreachableIssuerIsNonFatal pins the cold-start fix: when the
// OIDC issuer is not reachable at startup (e.g. the bundled Keycloak is still
// booting during a simultaneous rollout), the constructor must NOT return an
// error (previously it did → the service ran with a nil verifier that rejected
// every authenticated request forever). It must return a live verifier that
// fails closed on protected routes until background discovery succeeds.
func TestNewVerifier_UnreachableIssuerIsNonFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the background retry goroutine

	// 127.0.0.1:1 refuses instantly, so the bounded synchronous attempt fails
	// fast without waiting out the 5s timeout.
	v, err := NewVerifier(ctx, "http://127.0.0.1:1/realms/stube", "chino-web-beta,tv-web-beta")
	if err != nil {
		t.Fatalf("unreachable issuer must be non-fatal, got error: %v", err)
	}
	if v == nil {
		t.Fatal("expected a verifier, got nil")
	}
	if v.tokenVerifier() != nil {
		t.Fatal("verifier must not be ready before discovery completes")
	}

	// Fails closed: discovery hasn't completed, so protected routes are rejected
	// (503) even with a bearer token present. /healthz still bypasses.
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := v.Middleware(next)

	protected := httptest.NewRequest(http.MethodGet, "/catalog/items", nil)
	protected.Header.Set("Authorization", "Bearer sometoken")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, protected)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected fail-closed 503 before discovery completes, got %d", rr.Code)
	}

	health := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, health)
	if rr.Code != http.StatusOK {
		t.Fatalf("/healthz must bypass auth, got %d", rr.Code)
	}
}

// TestNewVerifier_ConfigErrorsStillReturned confirms the genuine
// misconfiguration cases (empty issuer / empty audiences) remain errors.
func TestNewVerifier_ConfigErrorsStillReturned(t *testing.T) {
	if _, err := NewVerifier(context.Background(), "", "chino"); err == nil {
		t.Fatal("empty issuer must return an error")
	}
	if _, err := NewVerifier(context.Background(), "http://issuer.example/realms/stube", "  ,  "); err == nil {
		t.Fatal("empty audiences must return an error")
	}
}
