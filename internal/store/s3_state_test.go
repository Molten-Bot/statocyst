package store

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"statocyst/internal/model"
)

type fakeS3State struct {
	mu      sync.Mutex
	objects map[string][]byte
	counts  fakeS3Counts
}

type fakeS3Counts struct {
	put    int
	get    int
	delete int
	list   int
}

func newFakeS3State() *fakeS3State {
	return &fakeS3State{
		objects: make(map[string][]byte),
	}
}

func (f *fakeS3State) server(bucket string) *httptest.Server {
	type obj struct {
		key string
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(path, bucket+"/") {
			key := strings.TrimPrefix(path, bucket+"/")
			switch r.Method {
			case http.MethodPut:
				body, _ := io.ReadAll(r.Body)
				f.mu.Lock()
				f.counts.put++
				f.objects[key] = body
				f.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				return
			case http.MethodGet:
				f.mu.Lock()
				f.counts.get++
				body, ok := f.objects[key]
				f.mu.Unlock()
				if !ok {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
				return
			case http.MethodDelete:
				f.mu.Lock()
				f.counts.delete++
				delete(f.objects, key)
				f.mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		if path == bucket && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			f.mu.Lock()
			f.counts.list++
			keys := make([]obj, 0, len(f.objects))
			for key := range f.objects {
				if strings.HasPrefix(key, prefix) {
					keys = append(keys, obj{key: key})
				}
			}
			f.mu.Unlock()
			sort.Slice(keys, func(i, j int) bool { return keys[i].key < keys[j].key })

			type content struct {
				Key string `xml:"Key"`
			}
			type listResult struct {
				XMLName     xml.Name  `xml:"ListBucketResult"`
				IsTruncated bool      `xml:"IsTruncated"`
				Contents    []content `xml:"Contents"`
			}
			out := listResult{IsTruncated: false}
			for _, key := range keys {
				out.Contents = append(out.Contents, content{Key: key.key})
			}
			w.WriteHeader(http.StatusOK)
			_ = xml.NewEncoder(w).Encode(out)
			return
		}

		http.NotFound(w, r)
	}))
}

func (f *fakeS3State) hasKey(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeS3State) deleteKey(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
}

func (f *fakeS3State) keysWithPrefix(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0)
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeS3State) resetCounts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts = fakeS3Counts{}
}

func (f *fakeS3State) currentCounts() fakeS3Counts {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts
}

func TestS3StateStore_ProfileAndPermissionsRoundTrip(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  server.Client(),
		endpoint:    server.URL,
		bucket:      "state-bucket",
		region:      "us-east-1",
		prefix:      "statocyst-state",
		pathStyle:   true,
	}

	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	id := &idGen{}

	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman(alice) failed: %v", err)
	}
	alice, err = store.UpdateHumanProfile(alice.HumanID, "alice", true, now)
	if err != nil {
		t.Fatalf("UpdateHumanProfile(alice) failed: %v", err)
	}
	alice, err = store.UpdateHumanMetadata(alice.HumanID, map[string]any{"public": true}, now)
	if err != nil {
		t.Fatalf("UpdateHumanMetadata(alice) failed: %v", err)
	}
	bob, err := store.UpsertHuman("dev", "bob-sub", "bob@b.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman(bob) failed: %v", err)
	}

	orgID := id.MustID(t)
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, orgID, now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}

	inviteID := id.MustID(t)
	invite, err := store.CreateInvite(org.OrgID, "bob@b.test", model.RoleMember, alice.HumanID, inviteID, "invite-secret-hash", now.Add(24*time.Hour), now, false)
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}
	if _, err := store.AcceptInvite(invite.InviteID, bob.HumanID, bob.Email, now, id.Next); err != nil {
		t.Fatalf("AcceptInvite failed: %v", err)
	}

	accessKeyID := id.MustID(t)
	accessKey, err := store.CreateOrgAccessKey(
		org.OrgID,
		"partner-read",
		[]string{model.OrgAccessScopeListHumans},
		nil,
		alice.HumanID,
		accessKeyID,
		"access-secret-hash",
		now,
		false,
	)
	if err != nil {
		t.Fatalf("CreateOrgAccessKey failed: %v", err)
	}
	if _, _, err := store.AuthorizeOrgAccessByName(org.Handle, accessKey.TokenHash, model.OrgAccessScopeListHumans, now); err != nil {
		t.Fatalf("AuthorizeOrgAccessByName failed: %v", err)
	}

	owner := alice.HumanID
	agent, err := store.RegisterAgent(org.OrgID, "agent-a", &owner, "agent-token-hash", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if _, err := store.UpdateAgentMetadata(agent.AgentUUID, map[string]any{"public": false}, alice.HumanID, now, false); err != nil {
		t.Fatalf("UpdateAgentMetadata failed: %v", err)
	}

	reloaded := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  server.Client(),
		endpoint:    server.URL,
		bucket:      "state-bucket",
		region:      "us-east-1",
		prefix:      "statocyst-state",
		pathStyle:   true,
	}
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	gotAlice, err := reloaded.GetHuman(alice.HumanID)
	if err != nil {
		t.Fatalf("GetHuman after reload failed: %v", err)
	}
	if gotAlice.Handle != "alice" {
		t.Fatalf("expected reloaded handle alice, got %q", gotAlice.Handle)
	}
	if gotAlice.HandleConfirmedAt == nil {
		t.Fatalf("expected reloaded confirmed handle")
	}

	bobMemberships := reloaded.ListMyMemberships(bob.HumanID)
	if len(bobMemberships) != 1 {
		t.Fatalf("expected bob to have 1 membership, got %d", len(bobMemberships))
	}
	orgHumans, err := reloaded.ListOrgHumans(org.OrgID, alice.HumanID, false)
	if err != nil {
		t.Fatalf("ListOrgHumans after reload failed: %v", err)
	}
	if len(orgHumans) != 2 {
		t.Fatalf("expected 2 humans in org after reload, got %d", len(orgHumans))
	}

	if _, _, err := reloaded.AuthorizeOrgAccessByName(org.Handle, "access-secret-hash", model.OrgAccessScopeListHumans, now); err != nil {
		t.Fatalf("AuthorizeOrgAccessByName after reload failed: %v", err)
	}
	if _, err := reloaded.AgentUUIDForTokenHash("agent-token-hash"); err != nil {
		t.Fatalf("AgentUUIDForTokenHash after reload failed: %v", err)
	}
}

func TestS3StateStore_PersistsSecondaryIndexes(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  server.Client(),
		endpoint:    server.URL,
		bucket:      "state-bucket",
		region:      "us-east-1",
		prefix:      "statocyst-state",
		pathStyle:   true,
	}
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 13, 0, 0, 0, time.UTC)
	id := &idGen{}
	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	alice, err = store.UpdateHumanProfile(alice.HumanID, "alice", true, now)
	if err != nil {
		t.Fatalf("UpdateHumanProfile failed: %v", err)
	}
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	owner := alice.HumanID
	_, err = store.RegisterAgent(org.OrgID, "agent-a", &owner, "agent-token-hash", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	requiredPrefixes := []string{
		"statocyst-state/idx/humans/by_auth/",
		"statocyst-state/idx/humans/by_handle/",
		"statocyst-state/idx/orgs/by_handle/",
		"statocyst-state/idx/memberships/by_org_human/",
		"statocyst-state/idx/agents/by_token_hash/",
		"statocyst-state/idx/agents/by_uri/",
	}
	for _, prefix := range requiredPrefixes {
		if !fake.hasKey(prefix) {
			t.Fatalf("expected index prefix persisted: %s", prefix)
		}
	}
}

func TestS3StateStore_ReloadRebuildsIndexesFromPrimaryState(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  server.Client(),
		endpoint:    server.URL,
		bucket:      "state-bucket",
		region:      "us-east-1",
		prefix:      "statocyst-state",
		pathStyle:   true,
	}
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := store.UpsertHuman("dev", "same-subject", "same@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman initial failed: %v", err)
	}

	// Simulate a stale/missing index object. Reload should rebuild from primaries.
	for _, key := range fake.keysWithPrefix("statocyst-state/idx/humans/by_auth/") {
		fake.deleteKey(key)
	}

	reloaded := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  server.Client(),
		endpoint:    server.URL,
		bucket:      "state-bucket",
		region:      "us-east-1",
		prefix:      "statocyst-state",
		pathStyle:   true,
	}
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	got, err := reloaded.UpsertHuman("dev", "same-subject", "same@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman after index drop failed: %v", err)
	}
	if got.HumanID != human.HumanID {
		t.Fatalf("expected human id %q after reload rebuild, got %q", human.HumanID, got.HumanID)
	}
}

func TestS3StateStore_IncrementalPersistAvoidsFullResync(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  server.Client(),
		endpoint:    server.URL,
		bucket:      "state-bucket",
		region:      "us-east-1",
		prefix:      "statocyst-state",
		pathStyle:   true,
	}
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC)
	id := &idGen{}
	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	owner := alice.HumanID
	agent, err := store.RegisterAgent(org.OrgID, "agent-a", &owner, "agent-token-hash", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	fake.resetCounts()
	if _, err := store.UpdateAgentMetadata(agent.AgentUUID, map[string]any{"public": false}, alice.HumanID, now, false); err != nil {
		t.Fatalf("UpdateAgentMetadata failed: %v", err)
	}

	counts := fake.currentCounts()
	if counts.list != 0 {
		t.Fatalf("expected no list requests during incremental persist, got %d", counts.list)
	}
	if counts.put > 3 {
		t.Fatalf("expected only changed objects to be written, got %d put requests", counts.put)
	}
}
