package auth

import "testing"

func TestIsSafeSupabaseBrowserKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		key  string
		want bool
	}{
		{name: "empty", key: "", want: false},
		{name: "publishable prefix", key: "sb_publishable_abcd", want: true},
		{name: "anon prefix", key: "sb_anon_abcd", want: true},
		{name: "secret prefix", key: "sb_secret_abcd", want: false},
		{name: "service role prefix", key: "sb_service_role_abcd", want: false},
		{name: "legacy anon jwt", key: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb2xlIjoiYW5vbiJ9.sig", want: true},
		{name: "legacy service jwt", key: "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb2xlIjoic2VydmljZV9yb2xlIn0.sig", want: false},
		{name: "non-jwt token", key: "not-a-jwt", want: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsSafeSupabaseBrowserKey(tc.key); got != tc.want {
				t.Fatalf("IsSafeSupabaseBrowserKey(%q)=%v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
