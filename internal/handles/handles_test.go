package handles

import (
	"strings"
	"testing"
)

func TestNormalize_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "letters numbers and separators", in: "  A!! B__C..D  ", want: "a-b_c.d"},
		{name: "collapses repeated separators", in: "__Alpha---Beta..", want: "alpha-beta"},
		{name: "trims edge separators", in: "---alpha---", want: "alpha"},
		{name: "non ascii normalized", in: "Cafe Team", want: "cafe-team"},
		{name: "max length 64", in: strings.Repeat("a", 70), want: strings.Repeat("a", 64)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateHandle_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		handle string
		ok     bool
	}{
		{name: "valid lower", handle: "ab", ok: true},
		{name: "valid mixed separators", handle: "alpha_1.beta-2", ok: true},
		{name: "too short", handle: "a", ok: false},
		{name: "starts with separator", handle: "-ab", ok: false},
		{name: "invalid chars", handle: "ab$cd", ok: false},
		{name: "blocked token", handle: "fuck", ok: false},
		{name: "blocked compacted", handle: "f.u.c.k", ok: false},
		{name: "blocked split", handle: "f-u-c-k", ok: false},
		{name: "safe substring", handle: "classical", ok: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHandle(tc.handle)
			if tc.ok && err != nil {
				t.Fatalf("expected %q valid, got err=%v", tc.handle, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected %q invalid", tc.handle)
			}
		})
	}
}

func TestNormalizeAgentRef_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "single handle", in: "  AGENT  ", want: "agent"},
		{name: "org slash agent", in: "Org Name/Agent.Name", want: "org-name/agent.name"},
		{name: "org slash human slash agent", in: "Org/Human Name/Agent", want: "org/human-name/agent"},
		{name: "extra slashes trimmed", in: "//Org//Human//Agent//", want: "org/human/agent"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeAgentRef(tc.in); got != tc.want {
				t.Fatalf("NormalizeAgentRef(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateAgentRef_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		ref  string
		ok   bool
	}{
		{name: "single handle", ref: "alpha-1", ok: true},
		{name: "single too short", ref: "a", ok: false},
		{name: "org slash agent", ref: "org/agent", ok: true},
		{name: "org slash human slash agent", ref: "org/human/agent", ok: true},
		{name: "wrong segments", ref: "org/human/agent/extra", ok: false},
		{name: "blocked segment", ref: "org/fuck", ok: false},
		{name: "short segment", ref: "o/agent", ok: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAgentRef(tc.ref)
			if tc.ok && err != nil {
				t.Fatalf("expected ref %q valid, got err=%v", tc.ref, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("expected ref %q invalid", tc.ref)
			}
		})
	}
}

func TestBuildAgentURI(t *testing.T) {
	t.Parallel()

	if got := BuildAgentURI("Org", nil, "Agent"); got != "org/agent" {
		t.Fatalf("unexpected org-owned URI: %q", got)
	}
	h := "Human"
	if got := BuildAgentURI("Org", &h, "Agent"); got != "org/human/agent" {
		t.Fatalf("unexpected human-owned URI: %q", got)
	}
}

func TestIsBlocked(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want bool
	}{
		{in: "fuck", want: true},
		{in: "f.u.c.k", want: true},
		{in: "f-u-c-k", want: true},
		{in: "safe-name", want: false},
	}
	for _, tc := range cases {
		if got := IsBlocked(tc.in); got != tc.want {
			t.Fatalf("IsBlocked(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}
