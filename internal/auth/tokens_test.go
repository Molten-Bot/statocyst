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
	if token == "" {
		t.Fatal("expected non-empty token")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not valid base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32 random bytes, got %d", len(decoded))
	}
}

func TestHashTokenIsStableAndSHA256Length(t *testing.T) {
	hashA := HashToken("same-token")
	hashB := HashToken("same-token")
	hashC := HashToken("different-token")

	if hashA == "" {
		t.Fatal("expected non-empty hash")
	}
	if hashA != hashB {
		t.Fatalf("expected stable hash for same input, got %q vs %q", hashA, hashB)
	}
	if hashA == hashC {
		t.Fatalf("expected different hashes for different inputs")
	}

	decoded, err := base64.RawURLEncoding.DecodeString(hashA)
	if err != nil {
		t.Fatalf("hash was not RawURLEncoding: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("expected 32-byte sha256 hash, got %d", len(decoded))
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{name: "valid", header: "Bearer abc123", want: "abc123"},
		{name: "trim spaces", header: "Bearer   abc123   ", want: "abc123"},
		{name: "missing prefix", header: "Token abc", wantErr: true},
		{name: "basic scheme", header: "Basic abc123", wantErr: true},
		{name: "empty token", header: "Bearer   ", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractBearerToken(tc.header)
			if tc.wantErr {
				if !errors.Is(err, ErrMissingBearer) {
					t.Fatalf("expected ErrMissingBearer, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ExtractBearerToken(%q)=%q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
