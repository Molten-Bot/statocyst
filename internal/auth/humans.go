package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var (
	ErrUnauthorizedHuman = errors.New("unauthorized human")
)

type HumanIdentity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
}

type HumanAuthProvider interface {
	Authenticate(*http.Request) (HumanIdentity, error)
	Name() string
}

func NewHumanAuthProviderFromEnv() HumanAuthProvider {
	name := strings.TrimSpace(strings.ToLower(os.Getenv("HUMAN_AUTH_PROVIDER")))
	switch name {
	case "supabase":
		return NewSupabaseAuthProvider(
			os.Getenv("SUPABASE_URL"),
			os.Getenv("SUPABASE_ANON_KEY"),
		)
	default:
		return NewDevHumanAuthProvider()
	}
}

type DevHumanAuthProvider struct{}

func NewDevHumanAuthProvider() *DevHumanAuthProvider {
	return &DevHumanAuthProvider{}
}

func (p *DevHumanAuthProvider) Name() string {
	return "dev"
}

func (p *DevHumanAuthProvider) Authenticate(r *http.Request) (HumanIdentity, error) {
	id := strings.TrimSpace(r.Header.Get("X-Human-Id"))
	if id == "" {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}
	email := strings.TrimSpace(strings.ToLower(r.Header.Get("X-Human-Email")))
	if email == "" {
		email = id + "@local.dev"
	}
	return HumanIdentity{
		Provider:      p.Name(),
		Subject:       id,
		Email:         email,
		EmailVerified: true,
	}, nil
}

type SupabaseAuthProvider struct {
	supabaseURL string
	anonKey     string
	httpClient  *http.Client
}

func NewSupabaseAuthProvider(supabaseURL, anonKey string) *SupabaseAuthProvider {
	return &SupabaseAuthProvider{
		supabaseURL: strings.TrimRight(strings.TrimSpace(supabaseURL), "/"),
		anonKey:     strings.TrimSpace(anonKey),
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (p *SupabaseAuthProvider) Name() string {
	return "supabase"
}

func (p *SupabaseAuthProvider) Authenticate(r *http.Request) (HumanIdentity, error) {
	if p.supabaseURL == "" || p.anonKey == "" {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}
	token, err := ExtractBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}

	req, err := http.NewRequest(http.MethodGet, p.supabaseURL+"/auth/v1/user", nil)
	if err != nil {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("apikey", p.anonKey)
	req.Header.Set("Accept", "application/json")

	res, err := p.httpClient.Do(req)
	if err != nil {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}

	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}

	var user struct {
		ID               string `json:"id"`
		Email            string `json:"email"`
		EmailConfirmedAt string `json:"email_confirmed_at"`
	}
	if err := json.Unmarshal(body, &user); err != nil {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}
	if strings.TrimSpace(user.ID) == "" {
		return HumanIdentity{}, ErrUnauthorizedHuman
	}

	return HumanIdentity{
		Provider:      p.Name(),
		Subject:       strings.TrimSpace(user.ID),
		Email:         strings.ToLower(strings.TrimSpace(user.Email)),
		EmailVerified: strings.TrimSpace(user.EmailConfirmedAt) != "",
	}, nil
}
