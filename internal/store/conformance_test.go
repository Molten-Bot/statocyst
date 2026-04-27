package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"moltenhub/internal/model"
)

type queueVectorSuite struct {
	Cases []queueVectorCase `json:"cases"`
}

type queueVectorCase struct {
	Name             string   `json:"name"`
	UnknownReceiver  bool     `json:"unknown_receiver"`
	Payloads         []string `json:"payloads"`
	ExpectError      string   `json:"expect_error,omitempty"`
	ExpectPopPayload []string `json:"expect_pop_payloads,omitempty"`
	ExpectEmptyAfter bool     `json:"expect_empty_after"`
}

type publishVectorSuite struct {
	Cases []publishVectorCase `json:"cases"`
}

type publishVectorCase struct {
	Name               string `json:"name"`
	SameOrg            bool   `json:"same_org"`
	ActivateOrgTrust   bool   `json:"activate_org_trust"`
	ActivateAgentTrust bool   `json:"activate_agent_trust"`
	BlockOrgTrust      bool   `json:"block_org_trust"`
	Expect             string `json:"expect"`
}

type conformanceStore interface {
	ControlPlaneStore
	MessageQueueStore
}

type conformanceBackend struct {
	Name string
	New  func(t *testing.T) conformanceStore
}

func TestMessageQueueConformanceVectors(t *testing.T) {
	suite := readQueueVectorSuite(t, "testdata/queue_vectors.json")
	for _, backend := range conformanceBackends() {
		backend := backend
		t.Run(backend.Name, func(t *testing.T) {
			for _, tc := range suite.Cases {
				tc := tc
				t.Run(tc.Name, func(t *testing.T) {
					mem := backend.New(t)
					ids := &idGen{}
					now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

					_, _, receiver := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")
					toAgentUUID := receiver.AgentUUID
					if tc.UnknownReceiver {
						toAgentUUID = "missing-agent-uuid"
					}

					for _, payload := range tc.Payloads {
						msg := model.Message{
							MessageID:     ids.MustID(t),
							FromAgentUUID: "sender-agent-uuid",
							ToAgentUUID:   toAgentUUID,
							SenderOrgID:   "org-sender",
							ReceiverOrgID: "org-receiver",
							ContentType:   "text/plain",
							Payload:       payload,
							CreatedAt:     now,
						}
						err := mem.Enqueue(context.Background(), msg)
						if tc.ExpectError != "" {
							if err == nil {
								t.Fatalf("expected enqueue error %q, got nil", tc.ExpectError)
							}
							if !strings.Contains(err.Error(), tc.ExpectError) {
								t.Fatalf("expected enqueue error containing %q, got %q", tc.ExpectError, err.Error())
							}
							return
						}
						if err != nil {
							t.Fatalf("enqueue failed: %v", err)
						}
					}

					for i, wantPayload := range tc.ExpectPopPayload {
						got, ok, err := mem.Dequeue(context.Background(), receiver.AgentUUID)
						if err != nil {
							t.Fatalf("dequeue %d failed: %v", i, err)
						}
						if !ok {
							t.Fatalf("pop %d missing, expected payload %q", i, wantPayload)
						}
						if got.Payload != wantPayload {
							t.Fatalf("pop %d payload mismatch: expected %q, got %q", i, wantPayload, got.Payload)
						}
					}

					if tc.ExpectEmptyAfter {
						if _, ok, err := mem.Dequeue(context.Background(), receiver.AgentUUID); err != nil {
							t.Fatalf("final dequeue failed: %v", err)
						} else if ok {
							t.Fatalf("expected empty queue after pops")
						}
					}
				})
			}
		})
	}
}

func TestCanPublishConformanceVectors(t *testing.T) {
	suite := readPublishVectorSuite(t, "testdata/publish_vectors.json")
	for _, backend := range conformanceBackends() {
		backend := backend
		t.Run(backend.Name, func(t *testing.T) {
			for _, tc := range suite.Cases {
				tc := tc
				t.Run(tc.Name, func(t *testing.T) {
					mem := backend.New(t)
					ids := &idGen{}
					now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

					alice, orgA, agentA := seedOrgAndAgent(t, mem, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")

					var bob model.Human
					var orgB model.Organization
					var agentB model.Agent

					if tc.SameOrg {
						owner := alice.HumanID
						var err error
						agentB, err = mem.RegisterAgent(orgA.OrgID, "agent-b", &owner, "token-agent-b", alice.HumanID, now, false)
						if err != nil {
							t.Fatalf("register same-org agent-b: %v", err)
						}
						bob = alice
					} else {
						bob, orgB, agentB = seedOrgAndAgent(t, mem, ids, now, "bob", "bob@b.test", "org-b", "Org B", "agent-b")
					}

					orgTrustID := ""
					if tc.ActivateOrgTrust && !tc.SameOrg {
						edge, _, err := mem.CreateOrJoinOrgTrust(orgA.OrgID, orgB.OrgID, alice.HumanID, ids.MustID(t), now, false)
						if err != nil {
							t.Fatalf("create org trust: %v", err)
						}
						orgTrustID = edge.EdgeID
						if _, err := mem.ApproveOrgTrust(edge.EdgeID, bob.HumanID, now, false); err != nil {
							t.Fatalf("approve org trust: %v", err)
						}
					}

					if tc.ActivateAgentTrust {
						edge, _, err := mem.CreateOrJoinAgentTrust("", agentA.AgentUUID, agentB.AgentUUID, alice.HumanID, ids.MustID(t), now, false)
						if err != nil {
							t.Fatalf("create agent trust: %v", err)
						}
						if edge.State != model.StatusActive {
							approver := bob.HumanID
							if tc.SameOrg {
								approver = alice.HumanID
							}
							if _, err := mem.ApproveAgentTrust(edge.EdgeID, approver, now, false); err != nil {
								t.Fatalf("approve agent trust: %v", err)
							}
						}
					}

					if tc.BlockOrgTrust && orgTrustID != "" {
						if _, err := mem.BlockOrgTrust(orgTrustID, bob.HumanID, now, false); err != nil {
							t.Fatalf("block org trust: %v", err)
						}
					}

					_, _, err := mem.CanPublish(agentA.AgentUUID, agentB.AgentUUID)
					switch tc.Expect {
					case "allow":
						if err != nil {
							t.Fatalf("expected publish allow, got error: %v", err)
						}
					case "deny_no_trust_path":
						if !errors.Is(err, ErrNoTrustPath) {
							t.Fatalf("expected ErrNoTrustPath, got: %v", err)
						}
					default:
						t.Fatalf("unknown expect value %q", tc.Expect)
					}
				})
			}
		})
	}
}

func TestA2AMessageRecordStorageConformance(t *testing.T) {
	for _, backend := range conformanceBackends() {
		backend := backend
		t.Run(backend.Name, func(t *testing.T) {
			st := backend.New(t)
			ids := &idGen{}
			now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

			_, orgA, agentA := seedOrgAndAgent(t, st, ids, now, "alice", "alice@a.test", "org-a", "Org A", "agent-a")
			_, orgB, agentB := seedOrgAndAgent(t, st, ids, now, "bob", "bob@b.test", "org-b", "Org B", "agent-b")

			clientMsgID := "a2a-client-message-1"
			msg := model.Message{
				MessageID:     ids.MustID(t),
				FromAgentUUID: agentA.AgentUUID,
				ToAgentUUID:   agentB.AgentUUID,
				FromAgentID:   agentA.AgentID,
				ToAgentID:     agentB.AgentID,
				SenderOrgID:   orgA.OrgID,
				ReceiverOrgID: orgB.OrgID,
				ContentType:   "application/json",
				Payload:       `{"protocol":"a2a.v1","message":{"messageId":"a2a-client-message-1","role":"ROLE_USER","parts":[{"text":"hello a2a"}]}}`,
				ClientMsgID:   &clientMsgID,
				CreatedAt:     now,
			}

			record, replay, err := st.CreateOrGetMessageRecord(msg, now)
			if err != nil {
				t.Fatalf("CreateOrGetMessageRecord failed: %v", err)
			}
			if replay {
				t.Fatalf("expected first CreateOrGetMessageRecord call to not replay")
			}
			if record.Status != model.MessageDeliveryQueued {
				t.Fatalf("expected queued record, got %#v", record)
			}

			replayed, replay, err := st.CreateOrGetMessageRecord(msg, now.Add(time.Second))
			if err != nil {
				t.Fatalf("CreateOrGetMessageRecord replay failed: %v", err)
			}
			if !replay || replayed.Message.MessageID != msg.MessageID || replayed.IdempotentReplays != 1 {
				t.Fatalf("expected idempotent replay of %q, got replay=%v record=%#v", msg.MessageID, replay, replayed)
			}

			loaded, err := st.GetMessageRecord(msg.MessageID)
			if err != nil {
				t.Fatalf("GetMessageRecord failed: %v", err)
			}
			if loaded.Message.ClientMsgID == nil || *loaded.Message.ClientMsgID != clientMsgID {
				t.Fatalf("expected client message id %q, got %#v", clientMsgID, loaded.Message.ClientMsgID)
			}

			for _, agentUUID := range []string{agentA.AgentUUID, agentB.AgentUUID} {
				records, err := st.ListMessageRecordsForAgent(agentUUID)
				if err != nil {
					t.Fatalf("ListMessageRecordsForAgent(%s) failed: %v", agentUUID, err)
				}
				if len(records) != 1 || records[0].Message.MessageID != msg.MessageID {
					t.Fatalf("expected one visible A2A message record for %s, got %#v", agentUUID, records)
				}
			}

			leaseAt := now.Add(2 * time.Second)
			delivery, leased, err := st.LeaseMessage(msg.MessageID, agentB.AgentUUID, ids.MustID(t), leaseAt, leaseAt.Add(time.Minute))
			if err != nil {
				t.Fatalf("LeaseMessage failed: %v", err)
			}
			if leased.Status != model.MessageDeliveryLeased || leased.DeliveryAttempts != 1 {
				t.Fatalf("expected leased record attempt 1, got %#v", leased)
			}

			acked, err := st.AckMessageDelivery(agentB.AgentUUID, delivery.DeliveryID, leaseAt.Add(time.Second))
			if err != nil {
				t.Fatalf("AckMessageDelivery failed: %v", err)
			}
			if acked.Status != model.MessageDeliveryAcked || acked.AckedAt == nil {
				t.Fatalf("expected acked record with ack timestamp, got %#v", acked)
			}
		})
	}
}

func conformanceBackends() []conformanceBackend {
	return []conformanceBackend{
		{
			Name: "memory",
			New: func(t *testing.T) conformanceStore {
				t.Helper()
				return NewMemoryStore()
			},
		},
		{
			Name: "s3_state",
			New: func(t *testing.T) conformanceStore {
				t.Helper()
				fake := newFakeS3State()
				server := fake.server("state-bucket")
				t.Cleanup(server.Close)
				st := newTestS3StateStore(t, server.Client(), server.URL, "state-bucket", "moltenhub-state")
				if err := st.loadFromS3(context.Background()); err != nil {
					t.Fatalf("loadFromS3 empty failed: %v", err)
				}
				return st
			},
		},
	}
}

func seedOrgAndAgent(
	t *testing.T,
	mem ControlPlaneStore,
	ids *idGen,
	now time.Time,
	humanSubject, humanEmail, orgHandle, orgDisplayName, agentID string,
) (model.Human, model.Organization, model.Agent) {
	t.Helper()

	human, err := mem.UpsertHuman("dev", humanSubject, humanEmail, true, now, ids.Next)
	if err != nil {
		t.Fatalf("upsert human %s: %v", humanSubject, err)
	}

	orgID := ids.MustID(t)
	org, _, err := mem.CreateOrg(orgHandle, orgDisplayName, human.HumanID, orgID, now)
	if err != nil {
		t.Fatalf("create org %s: %v", orgHandle, err)
	}

	ownerHumanID := human.HumanID
	agent, err := mem.RegisterAgent(org.OrgID, agentID, &ownerHumanID, "token-"+agentID, human.HumanID, now, false)
	if err != nil {
		t.Fatalf("register agent %s: %v", agentID, err)
	}

	return human, org, agent
}

type idGen struct {
	n int
}

func (g *idGen) Next() (string, error) {
	g.n++
	return fmt.Sprintf("id-%d", g.n), nil
}

func (g *idGen) MustID(t *testing.T) string {
	t.Helper()
	id, err := g.Next()
	if err != nil {
		t.Fatalf("generate id: %v", err)
	}
	return id
}

func readQueueVectorSuite(t *testing.T, path string) queueVectorSuite {
	t.Helper()
	var suite queueVectorSuite
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read queue vector fixture %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatalf("decode queue vector fixture %s: %v", path, err)
	}
	return suite
}

func readPublishVectorSuite(t *testing.T, path string) publishVectorSuite {
	t.Helper()
	var suite publishVectorSuite
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read publish vector fixture %s: %v", path, err)
	}
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatalf("decode publish vector fixture %s: %v", path, err)
	}
	return suite
}
