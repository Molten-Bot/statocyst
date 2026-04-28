package auth

import (
	"reflect"
	"testing"
)

func TestParseCSVSet(t *testing.T) {
	t.Parallel()

	got := ParseCSVSet(" Alice@Example.COM, @Example.org, ,alice@example.com ", true)
	want := map[string]struct{}{
		"alice@example.com": {},
		"example.org":       {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseCSVSet mismatch: got %#v want %#v", got, want)
	}
}

func TestSortedSetValues(t *testing.T) {
	t.Parallel()

	got := SortedSetValues(map[string]struct{}{"b": {}, "a": {}, "c": {}})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SortedSetValues mismatch: got %#v want %#v", got, want)
	}
}

func TestIsSuperAdmin(t *testing.T) {
	t.Parallel()

	emails := map[string]struct{}{"admin@example.com": {}}
	domains := map[string]struct{}{"example.org": {}}

	cases := []struct {
		name     string
		identity HumanIdentity
		want     bool
	}{
		{
			name:     "explicit email",
			identity: HumanIdentity{Email: " Admin@Example.com ", EmailVerified: true},
			want:     true,
		},
		{
			name:     "domain match",
			identity: HumanIdentity{Email: "owner@example.org", EmailVerified: true},
			want:     true,
		},
		{
			name:     "unverified",
			identity: HumanIdentity{Email: "admin@example.com", EmailVerified: false},
			want:     false,
		},
		{
			name:     "empty email",
			identity: HumanIdentity{EmailVerified: true},
			want:     false,
		},
		{
			name:     "invalid email",
			identity: HumanIdentity{Email: "invalid", EmailVerified: true},
			want:     false,
		},
		{
			name:     "unknown",
			identity: HumanIdentity{Email: "person@example.net", EmailVerified: true},
			want:     false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSuperAdmin(tc.identity, emails, domains); got != tc.want {
				t.Fatalf("IsSuperAdmin()=%v want %v", got, tc.want)
			}
		})
	}
}

func TestDomainFromEmail(t *testing.T) {
	t.Parallel()

	cases := []struct {
		email     string
		want      string
		wantFound bool
	}{
		{email: "local@example.com", want: "example.com", wantFound: true},
		{email: "@example.com", wantFound: false},
		{email: "local@", wantFound: false},
		{email: "local@example@com", wantFound: false},
		{email: "local", wantFound: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.email, func(t *testing.T) {
			t.Parallel()
			got, found := domainFromEmail(tc.email)
			if got != tc.want || found != tc.wantFound {
				t.Fatalf("domainFromEmail(%q)=(%q,%v) want (%q,%v)", tc.email, got, found, tc.want, tc.wantFound)
			}
		})
	}
}
