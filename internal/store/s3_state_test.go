package store

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"moltenhub/internal/model"
)

type fakeS3State struct {
	mu       sync.Mutex
	objects  map[string][]byte
	counts   fakeS3Counts
	putDelay time.Duration
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
				delay := f.putDelay
				f.counts.put++
				f.mu.Unlock()
				if delay > 0 {
					time.Sleep(delay)
				}
				f.mu.Lock()
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

func (f *fakeS3State) setPutDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putDelay = delay
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

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")

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

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
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

func TestS3StateStore_LoadFromS3HydratesAuditFeed(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := recentAuditTestTime()
	id := &idGen{}

	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	agent, err := store.RegisterAgent(org.OrgID, "agent-a", nil, "agent-token-hash", alice.HumanID, now.Add(time.Minute), false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	if !fake.hasKey("moltenhub-state/state/audit/") {
		t.Fatalf("expected persisted audit objects after org creation")
	}

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	snapshot := reloaded.AdminSnapshot()
	if len(snapshot.ActivityFeed) == 0 {
		t.Fatalf("expected non-empty AdminSnapshot activity_feed after reload")
	}
	foundOrgCreate := false
	foundAgentCreate := false
	for _, event := range snapshot.ActivityFeed {
		if event.Category == "org" && event.Action == "create" {
			foundOrgCreate = true
		}
		if event.Category == "agent" && event.Action == "create" && event.SubjectID == agent.AgentUUID {
			foundAgentCreate = true
		}
	}
	if !foundOrgCreate {
		t.Fatalf("expected org/create event in reloaded activity_feed, got=%v", snapshot.ActivityFeed)
	}
	if !foundAgentCreate {
		t.Fatalf("expected agent/create event in reloaded activity_feed, got=%v", snapshot.ActivityFeed)
	}
}

func TestS3StateStore_LoadFromS3HydratesArchivedDeletedEntities(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := recentAuditTestTime()
	id := &idGen{}

	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	agent, err := store.RegisterAgent(org.OrgID, "agent-a", nil, "agent-token-hash", alice.HumanID, now.Add(time.Minute), false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if err := store.DeleteOrg(org.OrgID, alice.HumanID, false, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("DeleteOrg failed: %v", err)
	}

	if !fake.hasKey("moltenhub-state/state/archived_orgs/") {
		t.Fatalf("expected persisted archived org object after delete")
	}
	if !fake.hasKey("moltenhub-state/state/archived_agents/") {
		t.Fatalf("expected persisted archived agent object after delete")
	}

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	snapshot := reloaded.AdminSnapshot()
	if _, ok := findArchivedSnapshotOrg(snapshot, org.OrgID); !ok {
		t.Fatalf("expected archived org after reload, got=%v", snapshot.ArchivedOrganizations)
	}
	archivedAgent, ok := findArchivedSnapshotAgent(snapshot, agent.AgentUUID)
	if !ok {
		t.Fatalf("expected archived agent after reload, got=%v", snapshot.ArchivedAgents)
	}
	if archivedAgent.Status != model.StatusDeleted || archivedAgent.RevokedAt == nil {
		t.Fatalf("expected archived agent status=deleted with revoked_at after reload, got=%+v", archivedAgent)
	}
	if _, ok := findAuditEvent(snapshot.ActivityFeed, "org", "delete", org.OrgID); !ok {
		t.Fatalf("expected org/delete event after reload, got=%v", snapshot.ActivityFeed)
	}
}

func TestS3StateStore_DoesNotPersistSecondaryIndexes(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
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

	if keys := fake.keysWithPrefix("moltenhub-state/idx/"); len(keys) != 0 {
		t.Fatalf("expected no persisted secondary index objects, got %v", keys)
	}
}

func TestS3StateStore_ReloadRebuildsIndexesFromPrimaryState(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 14, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := store.UpsertHuman("dev", "same-subject", "same@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman initial failed: %v", err)
	}

	if keys := fake.keysWithPrefix("moltenhub-state/idx/"); len(keys) != 0 {
		t.Fatalf("expected no persisted secondary index objects before reload, got %v", keys)
	}

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	got, err := reloaded.UpsertHuman("dev", "same-subject", "same@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman after reload failed: %v", err)
	}
	if got.HumanID != human.HumanID {
		t.Fatalf("expected human id %q after reload rebuild, got %q", human.HumanID, got.HumanID)
	}
}

func TestS3StateStore_ReloadRebuildsArchivedHandleReservations(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC)
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
	agent, err := store.RegisterAgent(org.OrgID, "agent-a", &owner, "agent-token-hash", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}
	if err := store.DeleteAgent(agent.AgentUUID, alice.HumanID, now.Add(time.Minute), false); err != nil {
		t.Fatalf("DeleteAgent failed: %v", err)
	}

	deletedOrg, _, err := store.CreateOrg("org-deleted", "Deleted Org", alice.HumanID, id.MustID(t), now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("CreateOrg deleted fixture failed: %v", err)
	}
	if err := store.DeleteOrg(deletedOrg.OrgID, alice.HumanID, false, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("DeleteOrg failed: %v", err)
	}

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	if _, err := reloaded.RegisterAgent(org.OrgID, "agent-a", &owner, "agent-token-hash-2", alice.HumanID, now.Add(4*time.Minute), false); !errors.Is(err, ErrAgentExists) {
		t.Fatalf("expected archived agent handle to remain reserved after reload with ErrAgentExists, got %v", err)
	}
	if _, _, err := reloaded.CreateOrg("org-deleted", "Takeover Org", alice.HumanID, id.MustID(t), now.Add(5*time.Minute)); !errors.Is(err, ErrOrgHandleTaken) {
		t.Fatalf("expected archived org handle to remain reserved after reload with ErrOrgHandleTaken, got %v", err)
	}
}

func TestRebuildStateIndexesReservesArchivedHumanHandles(t *testing.T) {
	mem := NewMemoryStore()
	mem.archivedHumans["deleted-human"] = model.Human{
		HumanID: "deleted-human",
		Handle:  "alice",
	}
	rebuildStateIndexesLocked(mem)

	now := time.Date(2026, 3, 5, 15, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := mem.UpsertHuman("dev", "alice", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	if human.Handle == "alice" {
		t.Fatalf("expected archived human handle to remain reserved, got reused handle %q", human.Handle)
	}
}

func TestS3StateStore_PersistsQueueRoundTripAndDeletePurge(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 14, 30, 0, 0, time.UTC)
	id := &idGen{}
	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman(alice) failed: %v", err)
	}
	bob, err := store.UpsertHuman("dev", "bob-sub", "bob@b.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman(bob) failed: %v", err)
	}
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	invite, err := store.CreateInvite(org.OrgID, bob.Email, model.RoleMember, alice.HumanID, id.MustID(t), "invite-secret", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}
	if _, err := store.AcceptInvite(invite.InviteID, bob.HumanID, bob.Email, now, id.Next); err != nil {
		t.Fatalf("AcceptInvite failed: %v", err)
	}
	aliceOwner := alice.HumanID
	bobOwner := bob.HumanID
	agentA, err := store.RegisterAgent(org.OrgID, "agent-a", &aliceOwner, "agent-a-token", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent(agent-a) failed: %v", err)
	}
	agentB, err := store.RegisterAgent(org.OrgID, "agent-b", &bobOwner, "agent-b-token", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent(agent-b) failed: %v", err)
	}

	msg1 := model.Message{
		MessageID:     "msg-1",
		FromAgentUUID: agentA.AgentUUID,
		ToAgentUUID:   agentB.AgentUUID,
		FromAgentID:   agentA.AgentID,
		ToAgentID:     agentB.AgentID,
		SenderOrgID:   org.OrgID,
		ReceiverOrgID: org.OrgID,
		ContentType:   "text/plain",
		Payload:       "hello",
		CreatedAt:     now.Add(time.Minute),
	}
	msg2 := model.Message{
		MessageID:     "msg-2",
		FromAgentUUID: agentB.AgentUUID,
		ToAgentUUID:   agentA.AgentUUID,
		FromAgentID:   agentB.AgentID,
		ToAgentID:     agentA.AgentID,
		SenderOrgID:   org.OrgID,
		ReceiverOrgID: org.OrgID,
		ContentType:   "text/plain",
		Payload:       "world",
		CreatedAt:     now.Add(2 * time.Minute),
	}
	if err := store.Enqueue(context.Background(), msg1); err != nil {
		t.Fatalf("Enqueue(msg1) failed: %v", err)
	}
	if err := store.Enqueue(context.Background(), msg2); err != nil {
		t.Fatalf("Enqueue(msg2) failed: %v", err)
	}

	queueKeys := fake.keysWithPrefix("moltenhub-state/state/queues/")
	if len(queueKeys) != 2 {
		t.Fatalf("expected 2 persisted queue objects, got %d", len(queueKeys))
	}

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	got, ok, err := reloaded.Dequeue(context.Background(), agentB.AgentUUID)
	if err != nil {
		t.Fatalf("Dequeue(agent-b) failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected queued message for agent-b")
	}
	if got.MessageID != msg1.MessageID {
		t.Fatalf("expected first dequeued message %q, got %q", msg1.MessageID, got.MessageID)
	}

	if err := reloaded.DeleteAgent(agentA.AgentUUID, alice.HumanID, now.Add(3*time.Minute), false); err != nil {
		t.Fatalf("DeleteAgent(agent-a) failed: %v", err)
	}

	reloadedAgain := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloadedAgain.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload after delete failed: %v", err)
	}

	if queue := reloadedAgain.MemoryStore.queues[agentA.AgentUUID]; len(queue) != 0 {
		t.Fatalf("expected deleted agent queue purged, got %d messages", len(queue))
	}
	if queue := reloadedAgain.MemoryStore.queues[agentB.AgentUUID]; len(queue) != 0 {
		t.Fatalf("expected messages referencing deleted agent purged, got %d messages", len(queue))
	}
	if keys := fake.keysWithPrefix("moltenhub-state/state/queues/"); len(keys) != 0 {
		t.Fatalf("expected queue objects removed after delete, got %d", len(keys))
	}
}

func TestS3StateStore_PersistsMessageRecordsAndLeases(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 5, 14, 45, 0, 0, time.UTC)
	id := &idGen{}
	alice, err := store.UpsertHuman("dev", "alice-sub", "alice@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman(alice) failed: %v", err)
	}
	bob, err := store.UpsertHuman("dev", "bob-sub", "bob@b.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman(bob) failed: %v", err)
	}
	org, _, err := store.CreateOrg("org-a", "Org A", alice.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	invite, err := store.CreateInvite(org.OrgID, bob.Email, model.RoleMember, alice.HumanID, id.MustID(t), "invite-secret", now.Add(time.Hour), now, false)
	if err != nil {
		t.Fatalf("CreateInvite failed: %v", err)
	}
	if _, err := store.AcceptInvite(invite.InviteID, bob.HumanID, bob.Email, now, id.Next); err != nil {
		t.Fatalf("AcceptInvite failed: %v", err)
	}
	aliceOwner := alice.HumanID
	bobOwner := bob.HumanID
	agentA, err := store.RegisterAgent(org.OrgID, "agent-a", &aliceOwner, "agent-a-token", alice.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent(agent-a) failed: %v", err)
	}
	agentB, err := store.RegisterAgent(org.OrgID, "agent-b", &bobOwner, "agent-b-token", bob.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent(agent-b) failed: %v", err)
	}

	clientMsgID := "client-msg-1"
	message := model.Message{
		MessageID:     "msg-lease-1",
		FromAgentUUID: agentA.AgentUUID,
		ToAgentUUID:   agentB.AgentUUID,
		FromAgentID:   agentA.AgentID,
		ToAgentID:     agentB.AgentID,
		SenderOrgID:   org.OrgID,
		ReceiverOrgID: org.OrgID,
		ContentType:   "text/plain",
		Payload:       "hello lease",
		ClientMsgID:   &clientMsgID,
		CreatedAt:     now.Add(time.Minute),
	}

	record, replay, err := store.CreateOrGetMessageRecord(message, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("CreateOrGetMessageRecord failed: %v", err)
	}
	if replay {
		t.Fatalf("expected first CreateOrGetMessageRecord call to not replay")
	}
	if err := store.Enqueue(context.Background(), message); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	dequeued, ok, err := store.Dequeue(context.Background(), agentB.AgentUUID)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected queued message for agent-b")
	}
	if dequeued.MessageID != message.MessageID {
		t.Fatalf("expected dequeued message %q, got %q", message.MessageID, dequeued.MessageID)
	}

	leasedAt := now.Add(3 * time.Minute)
	leaseExpiresAt := leasedAt.Add(60 * time.Second)
	delivery, leasedRecord, err := store.LeaseMessage(message.MessageID, agentB.AgentUUID, "delivery-1", leasedAt, leaseExpiresAt)
	if err != nil {
		t.Fatalf("LeaseMessage failed: %v", err)
	}
	if leasedRecord.Status != model.MessageDeliveryLeased {
		t.Fatalf("expected leased status, got %q", leasedRecord.Status)
	}

	if !fake.hasKey("moltenhub-state/state/messages/") {
		t.Fatalf("expected persisted message record objects")
	}
	if !fake.hasKey("moltenhub-state/state/message_leases/") {
		t.Fatalf("expected persisted message lease objects")
	}
	if keys := fake.keysWithPrefix("moltenhub-state/idx/"); len(keys) != 0 {
		t.Fatalf("expected no persisted secondary index objects, got %v", keys)
	}

	reloaded := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reloaded.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reload loadFromS3 failed: %v", err)
	}

	gotRecord, err := reloaded.GetMessageRecord(record.Message.MessageID)
	if err != nil {
		t.Fatalf("GetMessageRecord after reload failed: %v", err)
	}
	if gotRecord.Status != model.MessageDeliveryLeased {
		t.Fatalf("expected reloaded leased status, got %q", gotRecord.Status)
	}
	if gotRecord.LastDeliveryID == nil || *gotRecord.LastDeliveryID != delivery.DeliveryID {
		t.Fatalf("expected reloaded delivery id %q, got %#v", delivery.DeliveryID, gotRecord.LastDeliveryID)
	}
	if gotRecord.LeaseExpiresAt == nil || !gotRecord.LeaseExpiresAt.Equal(leaseExpiresAt) {
		t.Fatalf("expected reloaded lease expiry %v, got %#v", leaseExpiresAt, gotRecord.LeaseExpiresAt)
	}
	if gotRecord.Message.ClientMsgID == nil || *gotRecord.Message.ClientMsgID != clientMsgID {
		t.Fatalf("expected reloaded client msg id %q, got %#v", clientMsgID, gotRecord.Message.ClientMsgID)
	}

	metrics := reloaded.GetQueueMetrics()
	if metrics.AvailableMessages != 0 {
		t.Fatalf("expected no available messages after reload, got %d", metrics.AvailableMessages)
	}
	if metrics.LeasedMessages != 1 {
		t.Fatalf("expected 1 leased message after reload, got %d", metrics.LeasedMessages)
	}
	if metrics.OldestLeaseExpiryAt == nil || !metrics.OldestLeaseExpiryAt.Equal(leaseExpiresAt) {
		t.Fatalf("expected oldest lease expiry %v, got %#v", leaseExpiresAt, metrics.OldestLeaseExpiryAt)
	}

	ackedAt := now.Add(4 * time.Minute)
	ackedRecord, err := reloaded.AckMessageDelivery(agentB.AgentUUID, delivery.DeliveryID, ackedAt)
	if err != nil {
		t.Fatalf("AckMessageDelivery after reload failed: %v", err)
	}
	if ackedRecord.Status != model.MessageDeliveryAcked {
		t.Fatalf("expected acked status after reload, got %q", ackedRecord.Status)
	}
}

func TestS3StateStore_IncrementalPersistAvoidsFullResync(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
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

func TestS3StateStore_PersistAllAppliesDeadlineWhenContextHasNone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "state-bucket" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			type listResult struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				IsTruncated bool     `xml:"IsTruncated"`
			}
			w.WriteHeader(http.StatusOK)
			_ = xml.NewEncoder(w).Encode(listResult{IsTruncated: false})
			return
		}
		if strings.HasPrefix(path, "state-bucket/") && r.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			time.Sleep(500 * time.Millisecond)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	store.persistTimeout = 50 * time.Millisecond

	now := time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC)
	id := &idGen{}
	start := time.Now()
	_, err := store.UpsertHuman("dev", "slow-sub", "slow@a.test", true, now, id.Next)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected UpsertHuman to fail when S3 put blocks")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got: %v", err)
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("expected fast failure, took %s", elapsed)
	}
}

func TestS3StateStore_PersistAllUsesPerOperationDeadlineWhenContextHasNone(t *testing.T) {
	var (
		mu       sync.Mutex
		putCount int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "state-bucket" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			type listResult struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				IsTruncated bool     `xml:"IsTruncated"`
			}
			w.WriteHeader(http.StatusOK)
			_ = xml.NewEncoder(w).Encode(listResult{IsTruncated: false})
			return
		}
		if strings.HasPrefix(path, "state-bucket/") && r.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			time.Sleep(180 * time.Millisecond)
			mu.Lock()
			putCount++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	store.persistTimeout = 300 * time.Millisecond

	now := time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := store.UpsertHuman("dev", "slow-multi", "slow-multi@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("seed UpsertHuman failed: %v", err)
	}
	mu.Lock()
	putCount = 0
	mu.Unlock()

	start := time.Now()
	_, _, err = store.CreateOrg("slow-org", "Slow Org", human.HumanID, id.MustID(t), now)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected CreateOrg to succeed with per-operation timeout, got: %v", err)
	}
	if elapsed < 320*time.Millisecond {
		t.Fatalf("expected multi-object persist to exceed a single timeout window, took %s", elapsed)
	}
	mu.Lock()
	seenPuts := putCount
	mu.Unlock()
	if seenPuts < 2 {
		t.Fatalf("expected multiple state puts, saw %d", seenPuts)
	}
}

func TestS3StateStore_BestEffortPersistUsesShortTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if strings.HasPrefix(path, "state-bucket/") && r.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			time.Sleep(500 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			return
		}
		if path == "state-bucket" && r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			type listResult struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				IsTruncated bool     `xml:"IsTruncated"`
			}
			w.WriteHeader(http.StatusOK)
			_ = xml.NewEncoder(w).Encode(listResult{IsTruncated: false})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	store.bestEffortPersistTimeout = 50 * time.Millisecond
	store.persistTimeout = 5 * time.Second

	start := time.Now()
	store.RecordMessageQueued("org-best-effort")
	elapsed := time.Since(start)
	if elapsed > 750*time.Millisecond {
		t.Fatalf("expected best-effort persist to return quickly, took %s", elapsed)
	}
}

func TestS3StateStore_ExpireMessageLeasesUsesBestEffortPersistence(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := store.loadFromS3(context.Background()); err != nil {
		t.Fatalf("loadFromS3 empty failed: %v", err)
	}

	now := time.Date(2026, 3, 7, 8, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := store.UpsertHuman("dev", "lease-sub", "lease@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := store.CreateOrg("lease-org", "Lease Org", human.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	owner := human.HumanID
	agentA, err := store.RegisterAgent(org.OrgID, "lease-agent-a", &owner, "lease-token-a", human.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent(agent-a) failed: %v", err)
	}
	agentB, err := store.RegisterAgent(org.OrgID, "lease-agent-b", &owner, "lease-token-b", human.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent(agent-b) failed: %v", err)
	}

	message := model.Message{
		MessageID:     "expired-lease-message",
		FromAgentUUID: agentA.AgentUUID,
		ToAgentUUID:   agentB.AgentUUID,
		FromAgentID:   agentA.AgentID,
		ToAgentID:     agentB.AgentID,
		SenderOrgID:   org.OrgID,
		ReceiverOrgID: org.OrgID,
		ContentType:   "text/plain",
		Payload:       "hello expired lease",
		CreatedAt:     now.Add(time.Minute),
	}
	if _, _, err := store.CreateOrGetMessageRecord(message, now.Add(time.Minute)); err != nil {
		t.Fatalf("CreateOrGetMessageRecord failed: %v", err)
	}
	leasedAt := now.Add(2 * time.Minute)
	if _, _, err := store.LeaseMessage(message.MessageID, agentB.AgentUUID, "expired-delivery", leasedAt, leasedAt.Add(time.Second)); err != nil {
		t.Fatalf("LeaseMessage failed: %v", err)
	}

	store.bestEffortPersistTimeout = 50 * time.Millisecond
	store.persistTimeout = 5 * time.Second
	fake.setPutDelay(500 * time.Millisecond)

	start := time.Now()
	expired, err := store.ExpireMessageLeases(leasedAt.Add(time.Minute))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("ExpireMessageLeases returned error: %v", err)
	}
	if len(expired) != 1 || expired[0].MessageID != message.MessageID {
		t.Fatalf("expected one expired message %q, got %+v", message.MessageID, expired)
	}
	if elapsed > 750*time.Millisecond {
		t.Fatalf("expected best-effort lease expiry persistence to return quickly, took %s", elapsed)
	}

	record, err := store.GetMessageRecord(message.MessageID)
	if err != nil {
		t.Fatalf("GetMessageRecord failed: %v", err)
	}
	if record.Status != model.MessageDeliveryQueued {
		t.Fatalf("expected in-memory record to be queued after expiry, got %q", record.Status)
	}
}

func TestS3StateStore_SetAgentPresenceThrottlesHeartbeatPersistence(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	store := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	store.presencePersistInterval = 30 * time.Second

	now := time.Date(2026, 3, 7, 9, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := store.UpsertHuman("dev", "presence-sub", "presence@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := store.CreateOrg("presence-org", "Presence Org", human.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	owner := human.HumanID
	agent, err := store.RegisterAgent(org.OrgID, "presence-agent", &owner, "presence-token", human.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	fake.resetCounts()

	_, changed, err := store.SetAgentPresence(agent.AgentUUID, map[string]any{
		"status":      "online",
		"ready":       true,
		"transport":   "websocket",
		"session_key": "main",
	}, now)
	if err != nil {
		t.Fatalf("SetAgentPresence initial failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected first SetAgentPresence status write to mark changedStatus=true")
	}
	counts := fake.currentCounts()
	if counts.put != 1 {
		t.Fatalf("expected one presence put after initial online status, got %+v", counts)
	}

	presenceKeys := fake.keysWithPrefix("moltenhub-state/state/presence/")
	if len(presenceKeys) != 1 {
		t.Fatalf("expected one persisted presence object, got %v", presenceKeys)
	}
	fake.mu.Lock()
	presenceBody := append([]byte(nil), fake.objects[presenceKeys[0]]...)
	fake.mu.Unlock()
	var persisted map[string]any
	if err := json.Unmarshal(presenceBody, &persisted); err != nil {
		t.Fatalf("presence body decode failed: %v", err)
	}
	if updatedAt := strings.TrimSpace(stringValue(persisted["updated_at"])); updatedAt == "" {
		t.Fatalf("expected persisted presence to include normalized updated_at, got %v", persisted)
	}

	_, changed, err = store.SetAgentPresence(agent.AgentUUID, map[string]any{
		"status":      "online",
		"ready":       true,
		"transport":   "websocket",
		"session_key": "main",
	}, now.Add(10*time.Second))
	if err != nil {
		t.Fatalf("SetAgentPresence heartbeat failed: %v", err)
	}
	if changed {
		t.Fatalf("expected unchanged online heartbeat changedStatus=false")
	}
	counts = fake.currentCounts()
	if counts.put != 1 {
		t.Fatalf("expected heartbeat write inside interval to skip S3 put, got %+v", counts)
	}

	_, changed, err = store.SetAgentPresence(agent.AgentUUID, map[string]any{
		"status":      "online",
		"ready":       true,
		"transport":   "websocket",
		"session_key": "main",
	}, now.Add(40*time.Second))
	if err != nil {
		t.Fatalf("SetAgentPresence periodic heartbeat failed: %v", err)
	}
	if changed {
		t.Fatalf("expected periodic heartbeat to keep changedStatus=false while status unchanged")
	}
	counts = fake.currentCounts()
	if counts.put != 2 {
		t.Fatalf("expected heartbeat after interval to persist, got %+v", counts)
	}

	_, changed, err = store.SetAgentPresence(agent.AgentUUID, map[string]any{
		"status":      "offline",
		"ready":       false,
		"transport":   "websocket",
		"session_key": "main",
	}, now.Add(45*time.Second))
	if err != nil {
		t.Fatalf("SetAgentPresence offline transition failed: %v", err)
	}
	if !changed {
		t.Fatalf("expected offline transition to mark changedStatus=true")
	}
	counts = fake.currentCounts()
	if counts.put != 3 {
		t.Fatalf("expected status transition to persist immediately, got %+v", counts)
	}
}

func TestS3StateStore_GetAgentPresenceRefreshesFromS3(t *testing.T) {
	fake := newFakeS3State()
	server := fake.server("state-bucket")
	defer server.Close()

	writer := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")

	now := time.Date(2026, 3, 7, 10, 0, 0, 0, time.UTC)
	id := &idGen{}
	human, err := writer.UpsertHuman("dev", "presence-reader-sub", "presence-reader@a.test", true, now, id.Next)
	if err != nil {
		t.Fatalf("UpsertHuman failed: %v", err)
	}
	org, _, err := writer.CreateOrg("presence-reader-org", "Presence Reader Org", human.HumanID, id.MustID(t), now)
	if err != nil {
		t.Fatalf("CreateOrg failed: %v", err)
	}
	owner := human.HumanID
	agent, err := writer.RegisterAgent(org.OrgID, "presence-reader-agent", &owner, "presence-reader-token", human.HumanID, now, false)
	if err != nil {
		t.Fatalf("RegisterAgent failed: %v", err)
	}

	reader := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
	if err := reader.loadFromS3(context.Background()); err != nil {
		t.Fatalf("reader loadFromS3 failed: %v", err)
	}
	if presence, ok, err := reader.GetAgentPresence(agent.AgentUUID); err != nil || ok || presence != nil {
		t.Fatalf("expected no reader presence before writer update, got presence=%v ok=%v err=%v", presence, ok, err)
	}

	if _, _, err := writer.SetAgentPresence(agent.AgentUUID, map[string]any{
		"status":      "online",
		"ready":       true,
		"transport":   "websocket",
		"session_key": "main",
	}, now.Add(1*time.Second)); err != nil {
		t.Fatalf("writer SetAgentPresence online failed: %v", err)
	}

	presence, ok, err := reader.GetAgentPresence(agent.AgentUUID)
	if err != nil {
		t.Fatalf("reader GetAgentPresence online failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected reader presence after writer update")
	}
	if got := stringValue(presence["status"]); got != "online" {
		t.Fatalf("expected reader to refresh online presence, got %q payload=%v", got, presence)
	}
	if ready, _ := presence["ready"].(bool); !ready {
		t.Fatalf("expected reader ready=true, got payload=%v", presence)
	}

	if _, _, err := writer.SetAgentPresence(agent.AgentUUID, map[string]any{
		"status":      "offline",
		"ready":       false,
		"transport":   "websocket",
		"session_key": "main",
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("writer SetAgentPresence offline failed: %v", err)
	}

	presence, ok, err = reader.GetAgentPresence(agent.AgentUUID)
	if err != nil {
		t.Fatalf("reader GetAgentPresence offline failed: %v", err)
	}
	if !ok {
		t.Fatalf("expected reader offline presence after writer update")
	}
	if got := stringValue(presence["status"]); got != "offline" {
		t.Fatalf("expected reader to refresh offline presence, got %q payload=%v", got, presence)
	}
	if ready, _ := presence["ready"].(bool); ready {
		t.Fatalf("expected reader ready=false, got payload=%v", presence)
	}
}
