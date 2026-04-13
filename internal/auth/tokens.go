package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

var ErrMissingBearer = errors.New("missing bearer token")

const (
	bindTokenPrefix  = "b_"
	agentTokenPrefix = "t_"
)

func generateTokenWithPrefix(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func GenerateToken() (string, error) {
	return generateTokenWithPrefix("")
}

func GenerateBindToken() (string, error) {
	return generateTokenWithPrefix(bindTokenPrefix)
}

func GenerateAgentToken() (string, error) {
	return generateTokenWithPrefix(agentTokenPrefix)
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func ExtractBearerToken(headerValue string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(headerValue, prefix) {
		return "", ErrMissingBearer
	}
	token := strings.TrimSpace(strings.TrimPrefix(headerValue, prefix))
	if token == "" {
		return "", ErrMissingBearer
	}
	return token, nil
}
