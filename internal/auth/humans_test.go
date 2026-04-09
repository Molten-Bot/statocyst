package auth

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (errReadCloser) Close() error             { return nil }

func TestNewHumanAuthProviderFromEnv(t *testing.T) {
	t.Setenv("HUMAN_AUTH_PROVIDER", "supabase")
	t.Setenv("SUPABASE_URL", "https://example.supabase.co")
	t.Setenv("SUPABASE_ANON_KEY", "anon-key")
	if got := NewHumanAuthProviderFromEnv().Name(); got != "supabase" {
		t.Fatalf("expected supabase provider, got %q", got)
	}

	t.Setenv("HUMAN_AUTH_PROVIDER", "")
	if got := NewHumanAuthProviderFromEnv().Name(); got != "dev" {
		t.Fatalf("expected default dev provider, got %q", got)
	}
}

func TestDevHumanAuthProviderAuthenticate(t *testing.T) {
	provider := NewDevHumanAuthProvider()
	if provider.Name() != "dev" {
		t.Fatalf("expected dev provider name, got %q", provider.Name())
	}

	reqMissing := &http.Request{Header: http.Header{}}
	if _, err := provider.Authenticate(reqMissing); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized error, got %v", err)
	}

	req := &http.Request{Header: http.Header{}}
	req.Header.Set("X-Human-Id", "alice")
	identity, err := provider.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected auth error: %v", err)
	}
	if identity.Subject != "alice" || identity.Email != "alice@local.dev" || !identity.EmailVerified {
		t.Fatalf("unexpected identity from implicit email: %+v", identity)
	}

	reqExplicitEmail := &http.Request{Header: http.Header{}}
	reqExplicitEmail.Header.Set("X-Human-Id", "bob")
	reqExplicitEmail.Header.Set("X-Human-Email", "  BOB@Example.COM  ")
	identity, err = provider.Authenticate(reqExplicitEmail)
	if err != nil {
		t.Fatalf("unexpected auth error with explicit email: %v", err)
	}
	if identity.Email != "bob@example.com" {
		t.Fatalf("expected lower-cased explicit email, got %+v", identity)
	}
}

func TestSupabaseAuthProviderAuthenticateUnauthorizedPaths(t *testing.T) {
	request := func() *http.Request {
		req := &http.Request{Header: http.Header{}}
		req.Header.Set("Authorization", "Bearer token")
		return req
	}

	if _, err := NewSupabaseAuthProvider("", "").Authenticate(request()); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized for missing config, got %v", err)
	}

	reqMissingBearer := &http.Request{Header: http.Header{}}
	provider := NewSupabaseAuthProvider("https://example.supabase.co", "anon")
	if _, err := provider.Authenticate(reqMissingBearer); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized for missing bearer, got %v", err)
	}

	provider = NewSupabaseAuthProvider("://bad-url", "anon")
	if _, err := provider.Authenticate(request()); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized for request-build failure, got %v", err)
	}

	provider = NewSupabaseAuthProvider("https://example.supabase.co", "anon")
	provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}
	if _, err := provider.Authenticate(request()); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized on transport error, got %v", err)
	}

	provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":"unauthorized"}`))}, nil
	})}
	if _, err := provider.Authenticate(request()); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized for non-200 status, got %v", err)
	}

	provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: errReadCloser{}}, nil
	})}
	if _, err := provider.Authenticate(request()); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized for body read error, got %v", err)
	}

	provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":""}`))}, nil
	})}
	if _, err := provider.Authenticate(request()); !errors.Is(err, ErrUnauthorizedHuman) {
		t.Fatalf("expected unauthorized for empty id, got %v", err)
	}
}

func TestSupabaseAuthProviderAuthenticateSuccess(t *testing.T) {
	provider := NewSupabaseAuthProvider("https://example.supabase.co", "anon")
	provider.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer valid-token" {
			t.Fatalf("expected forwarded bearer token, got %q", got)
		}
		if got := req.Header.Get("apikey"); got != "anon" {
			t.Fatalf("expected apikey header, got %q", got)
		}
		body := `{"id":"user-123","email":"USER@EXAMPLE.COM","email_confirmed_at":"2026-04-08T00:00:00Z"}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}

	req := &http.Request{Header: http.Header{}}
	req.Header.Set("Authorization", "Bearer valid-token")

	identity, err := provider.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected auth error: %v", err)
	}
	if identity.Provider != "supabase" || identity.Subject != "user-123" || identity.Email != "user@example.com" || !identity.EmailVerified {
		t.Fatalf("unexpected identity: %+v", identity)
	}

	provider.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"id":"user-456","email":"new@example.com","email_confirmed_at":""}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	identity, err = provider.Authenticate(req)
	if err != nil {
		t.Fatalf("unexpected auth error on unverified email case: %v", err)
	}
	if identity.EmailVerified {
		t.Fatalf("expected unverified email when email_confirmed_at is empty, got %+v", identity)
	}
}
