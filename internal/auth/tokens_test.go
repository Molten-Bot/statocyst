package auth

import (
	"encoding/base64"
	"errors"
	"testing"
)

func TestGenerateTokenProducesURLSafeEntropy(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token was not RawURLEncoding: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32 random bytes, got %d", len(decoded))
	}
}

func TestHashTokenIsStableAndSHA256Length(t *testing.T) {
	h1 := HashToken("same-token")
	h2 := HashToken("same-token")
	h3 := HashToken("different-token")
	if h1 != h2 {
		t.Fatalf("expected stable hash for same input, got %q vs %q", h1, h2)
	}
	if h1 == h3 {
		t.Fatalf("expected different hashes for different inputs")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(h1)
	if err != nil {
		t.Fatalf("hash was not RawURLEncoding: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32-byte sha256 hash, got %d", len(decoded))
	}
}

func TestExtractBearerToken(t *testing.T) {
	token, err := ExtractBearerToken("Bearer abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "abc123" {
		t.Fatalf("expected token abc123, got %q", token)
	}

	if _, err := ExtractBearerToken("Basic abc123"); !errors.Is(err, ErrMissingBearer) {
		t.Fatalf("expected ErrMissingBearer for non-bearer auth, got %v", err)
	}

	if _, err := ExtractBearerToken("Bearer    "); !errors.Is(err, ErrMissingBearer) {
		t.Fatalf("expected ErrMissingBearer for empty bearer token, got %v", err)
	}
}
