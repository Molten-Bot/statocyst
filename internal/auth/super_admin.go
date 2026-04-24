package auth

import (
	"sort"
	"strings"
)

// ParseCSVSet normalizes comma-separated values into a unique lowercase set.
// When trimLeadingAt is true, a single leading '@' is removed from each value.
func ParseCSVSet(csv string, trimLeadingAt bool) map[string]struct{} {
	out := make(map[string]struct{})
	for _, raw := range strings.Split(csv, ",") {
		value := strings.ToLower(strings.TrimSpace(raw))
		if trimLeadingAt {
			value = strings.TrimPrefix(value, "@")
		}
		if value == "" {
			continue
		}
		out[value] = struct{}{}
	}
	return out
}

func SortedSetValues(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func IsSuperAdmin(identity HumanIdentity, superAdminEmails, superAdminDomains map[string]struct{}) bool {
	if !identity.EmailVerified {
		return false
	}
	email := strings.ToLower(strings.TrimSpace(identity.Email))
	if email == "" {
		return false
	}
	if _, ok := superAdminEmails[email]; ok {
		return true
	}

	domain, ok := domainFromEmail(email)
	if !ok {
		return false
	}
	_, ok = superAdminDomains[domain]
	return ok
}

func domainFromEmail(email string) (string, bool) {
	local, domain, found := strings.Cut(email, "@")
	if !found || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", false
	}
	return domain, true
}
