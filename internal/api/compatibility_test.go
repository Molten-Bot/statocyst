package api

import (
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
)

type openAPIOperation struct {
	Method string
	Path   string
}

func parseOpenAPIOperations(t *testing.T) []openAPIOperation {
	t.Helper()

	lines := strings.Split(string(openapiYAML), "\n")
	inPaths := false
	currentPath := ""
	ops := make([]openAPIOperation, 0)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inPaths {
			if trimmed == "paths:" {
				inPaths = true
			}
			continue
		}

		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}

		if strings.HasPrefix(line, "  /") && strings.HasSuffix(trimmed, ":") {
			currentPath = strings.TrimSuffix(trimmed, ":")
			continue
		}

		if currentPath == "" {
			continue
		}

		switch trimmed {
		case "get:", "post:", "patch:", "delete:", "put:", "head:", "options:":
			ops = append(ops, openAPIOperation{
				Method: strings.ToUpper(strings.TrimSuffix(trimmed, ":")),
				Path:   currentPath,
			})
		}
	}

	if len(ops) == 0 {
		t.Fatalf("expected at least one operation parsed from openapi.yaml")
	}
	return ops
}

func parseOpenAPIPaths(t *testing.T) []string {
	t.Helper()
	seen := map[string]struct{}{}
	for _, op := range parseOpenAPIOperations(t) {
		seen[op.Path] = struct{}{}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func parseRouterRegisteredPaths(t *testing.T) []string {
	t.Helper()

	content, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("read router.go: %v", err)
	}

	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "mux.HandleFunc(\"") {
			continue
		}
		rest := strings.TrimPrefix(line, "mux.HandleFunc(\"")
		end := strings.Index(rest, "\"")
		if end <= 0 {
			continue
		}
		path := rest[:end]
		if path == "/health" || path == "/openapi.yaml" || strings.HasPrefix(path, "/v1/") {
			seen[path] = struct{}{}
		}
	}

	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func makeConcretePath(specPath string) string {
	replacements := map[string]string{
		"{org_id}":     "org-test",
		"{invite_id}":  "invite-test",
		"{key_id}":     "key-test",
		"{human_id}":   "human-test",
		"{agent_id}":   "agent-test",
		"{agent_uuid}": "11111111-1111-1111-1111-111111111111",
		"{id}":         "edge-test",
	}
	path := specPath
	for from, to := range replacements {
		path = strings.ReplaceAll(path, from, to)
	}
	return path
}

func TestAPIModelContract_OnboardingAndOrganizationShape(t *testing.T) {
	router := newTestRouter()

	before := doJSONRequest(t, router, http.MethodGet, "/v1/me", nil, humanHeaders("alice", "alice@a.test"))
	if before.Code != http.StatusOK {
		t.Fatalf("/v1/me before onboarding failed: %d %s", before.Code, before.Body.String())
	}
	beforePayload := decodeJSONMap(t, before.Body.Bytes())
	onboardingBefore, ok := beforePayload["onboarding"].(map[string]any)
	if !ok {
		t.Fatalf("missing onboarding object: %v", beforePayload)
	}
	if onboardingBefore["handle_required"] != true {
		t.Fatalf("expected onboarding.handle_required=true, got %v", onboardingBefore["handle_required"])
	}
	if onboardingBefore["handle_confirmed"] != false {
		t.Fatalf("expected onboarding.handle_confirmed=false, got %v", onboardingBefore["handle_confirmed"])
	}
	if onboardingBefore["next_step"] != "set_handle" {
		t.Fatalf("expected onboarding.next_step=set_handle, got %v", onboardingBefore["next_step"])
	}

	afterPatch := doJSONRequest(t, router, http.MethodPatch, "/v1/me", map[string]any{
		"handle": "alice",
	}, humanHeaders("alice", "alice@a.test"))
	if afterPatch.Code != http.StatusOK {
		t.Fatalf("/v1/me patch failed: %d %s", afterPatch.Code, afterPatch.Body.String())
	}
	afterPayload := decodeJSONMap(t, afterPatch.Body.Bytes())
	humanObj, ok := afterPayload["human"].(map[string]any)
	if !ok {
		t.Fatalf("missing human object in patch response: %v", afterPayload)
	}
	if humanObj["handle"] != "alice" {
		t.Fatalf("expected human.handle=alice, got %v", humanObj["handle"])
	}
	if confirmedAt, _ := humanObj["handle_confirmed_at"].(string); strings.TrimSpace(confirmedAt) == "" {
		t.Fatalf("expected non-empty human.handle_confirmed_at, got %v", humanObj["handle_confirmed_at"])
	}
	onboardingAfter, ok := afterPayload["onboarding"].(map[string]any)
	if !ok {
		t.Fatalf("missing onboarding object after patch: %v", afterPayload)
	}
	if onboardingAfter["handle_confirmed"] != true {
		t.Fatalf("expected onboarding.handle_confirmed=true after patch, got %v", onboardingAfter["handle_confirmed"])
	}

	createOrgResp := doJSONRequest(t, router, http.MethodPost, "/v1/orgs", map[string]any{
		"handle":       "alpha-team",
		"display_name": "Alpha Team",
	}, humanHeaders("alice", "alice@a.test"))
	if createOrgResp.Code != http.StatusCreated {
		t.Fatalf("create org failed: %d %s", createOrgResp.Code, createOrgResp.Body.String())
	}
	createOrgPayload := decodeJSONMap(t, createOrgResp.Body.Bytes())
	orgObj, ok := createOrgPayload["organization"].(map[string]any)
	if !ok {
		t.Fatalf("missing organization object: %v", createOrgPayload)
	}
	orgID, _ := orgObj["org_id"].(string)
	if strings.TrimSpace(orgID) == "" {
		t.Fatalf("expected organization.org_id, got %v", orgObj["org_id"])
	}
	if orgObj["handle"] != "alpha-team" {
		t.Fatalf("expected organization.handle=alpha-team, got %v", orgObj["handle"])
	}
	if orgObj["display_name"] != "Alpha Team" {
		t.Fatalf("expected organization.display_name=Alpha Team, got %v", orgObj["display_name"])
	}

	myOrgsResp := doJSONRequest(t, router, http.MethodGet, "/v1/me/orgs", nil, humanHeaders("alice", "alice@a.test"))
	if myOrgsResp.Code != http.StatusOK {
		t.Fatalf("/v1/me/orgs failed: %d %s", myOrgsResp.Code, myOrgsResp.Body.String())
	}
	myOrgsPayload := decodeJSONMap(t, myOrgsResp.Body.Bytes())
	memberships, _ := myOrgsPayload["memberships"].([]any)
	if len(memberships) == 0 {
		t.Fatalf("expected at least one membership in /v1/me/orgs")
	}

	foundNewOrg := false
	for _, item := range memberships {
		row, _ := item.(map[string]any)
		orgRow, _ := row["org"].(map[string]any)
		if orgRow["org_id"] == orgID {
			foundNewOrg = true
			if orgRow["handle"] != "alpha-team" || orgRow["display_name"] != "Alpha Team" {
				t.Fatalf("unexpected org row shape: %v", orgRow)
			}
			break
		}
	}
	if !foundNewOrg {
		t.Fatalf("newly created org %q not present in /v1/me/orgs response", orgID)
	}
}

func TestOpenAPISpecCoversRegisteredAPIRoutes(t *testing.T) {
	routerPaths := parseRouterRegisteredPaths(t)
	specPaths := parseOpenAPIPaths(t)

	specSet := make(map[string]struct{}, len(specPaths))
	for _, p := range specPaths {
		specSet[p] = struct{}{}
	}

	missing := make([]string, 0)
	for _, rp := range routerPaths {
		if strings.HasSuffix(rp, "/") {
			matched := false
			for _, sp := range specPaths {
				if strings.HasPrefix(sp, rp) && len(sp) > len(rp) {
					matched = true
					break
				}
			}
			if !matched {
				missing = append(missing, rp)
			}
			continue
		}
		if _, ok := specSet[rp]; !ok {
			missing = append(missing, rp)
		}
	}

	if len(missing) > 0 {
		t.Fatalf("openapi.yaml missing coverage for registered API routes: %v", missing)
	}
}

func TestOpenAPIOperationsReachImplementedHandlers(t *testing.T) {
	router := newTestRouter()
	for _, op := range parseOpenAPIOperations(t) {
		path := makeConcretePath(op.Path)
		if strings.HasPrefix(path, "/v1/org-access/") {
			path += "?org_name=org-test"
		}

		var body any
		switch op.Method {
		case http.MethodPost, http.MethodPatch, http.MethodPut:
			body = map[string]any{}
		}

		resp := doJSONRequest(t, router, op.Method, path, body, nil)

		if resp.Code == http.StatusMethodNotAllowed {
			t.Fatalf("operation %s %s returned 405; spec and implementation are out of sync", op.Method, op.Path)
		}
		if resp.Code == http.StatusNotFound {
			payload := decodeJSONMap(t, resp.Body.Bytes())
			if payload["error"] == "not_found" {
				t.Fatalf("operation %s %s hit router not_found; spec and implementation are out of sync", op.Method, op.Path)
			}
		}
		if resp.Code >= 500 {
			t.Fatalf("operation %s %s returned server error %d body=%s", op.Method, op.Path, resp.Code, resp.Body.String())
		}
	}
}
