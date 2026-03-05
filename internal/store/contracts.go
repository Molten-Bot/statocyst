package store

import (
	"context"
	"time"

	"statocyst/internal/model"
)

// ControlPlaneStore captures all non-queue state operations used by handlers.
type ControlPlaneStore interface {
	UpsertHuman(provider, subject, email string, emailVerified bool, now time.Time, idFactory func() (string, error)) (model.Human, error)
	UpdateHumanProfile(humanID, handle string, isPublic *bool, confirmHandle bool, now time.Time) (model.Human, error)
	CreateOrg(handle, displayName string, creatorHumanID string, orgID string, now time.Time) (model.Organization, model.Membership, error)
	EnsurePersonalOrg(humanID string, now time.Time, idFactory func() (string, error)) (model.Organization, error)
	ListMyMemberships(humanID string) []model.MembershipWithOrg
	CreateInvite(orgID, email, role, actorHumanID, inviteID, inviteSecretHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.Invite, error)
	AcceptInvite(inviteID, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error)
	AcceptInviteBySecretHash(inviteSecretHash, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error)
	ListInvitesForHuman(humanID, humanEmail string, isSuperAdmin bool) []model.InviteWithOrg
	ListOrgInvites(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.Invite, error)
	RevokeInvite(inviteID, actorHumanID, actorEmail string, isSuperAdmin bool, now time.Time) (model.Invite, error)
	RevokeMembership(orgID, humanID, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Membership, error)
	CreateOrgAccessKey(
		orgID, label string,
		scopes []string,
		expiresAt *time.Time,
		actorHumanID, keyID, tokenHash string,
		now time.Time,
		isSuperAdmin bool,
	) (model.OrgAccessKey, error)
	ListOrgAccessKeys(orgID, actorHumanID string, isSuperAdmin bool) ([]model.OrgAccessKey, error)
	RevokeOrgAccessKey(orgID, keyID, actorHumanID string, isSuperAdmin bool, now time.Time) (model.OrgAccessKey, error)
	AuthorizeOrgAccessByName(orgName, accessKeyHash, requiredScope string, now time.Time) (model.Organization, model.OrgAccessKey, error)
	ListOrgHumans(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.OrgHumanView, error)
	RegisterAgent(orgID, agentID string, ownerHumanID *string, tokenHash, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error)
	CreateBindToken(orgID string, ownerHumanID *string, actorHumanID, bindID, bindTokenHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.BindToken, error)
	RedeemBindToken(bindTokenHash, agentID, agentTokenHash string, now time.Time) (model.Agent, error)
	RotateAgentToken(agentUUID, actorHumanID, tokenHash string, now time.Time, isSuperAdmin bool) error
	RevokeAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error
	SetOrgVisibility(orgID string, isPublic bool, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Organization, error)
	SetAgentVisibility(agentUUID string, isPublic bool, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error)
	AgentUUIDForTokenHash(tokenHash string) (string, error)
	GetHuman(humanID string) (model.Human, error)
	GetAgentByUUID(agentUUID string) (model.Agent, error)
	GetAgentURI(agentUUID string) (string, error)
	ResolveAgentUUID(agentRef string) (string, error)
	CountActiveHumanOwnedAgents(humanID string) int
	PeekBindToken(bindTokenHash string) (model.BindToken, error)
	ListTalkablePeers(agentUUID string) ([]string, error)
	CreateOrJoinOrgTrust(orgID, peerOrgID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error)
	CreateOrJoinAgentTrust(orgID, agentUUID, peerAgentUUID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error)
	ApproveOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error)
	BlockOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error)
	RevokeOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error)
	ApproveAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error)
	BlockAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error)
	RevokeAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error)
	ListOrgAgents(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.Agent, error)
	ListHumanAgents(humanID string) []model.Agent
	ListHumanAgentTrusts(humanID string) []model.TrustEdge
	ListOrgTrustGraph(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.TrustEdge, []model.TrustEdge, error)
	ListAudit(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.AuditEvent, error)
	GetOrgStats(orgID, requesterHumanID string, isSuperAdmin bool) (model.OrgStats, error)
	AdminSnapshot() model.AdminSnapshot
	CanPublish(senderAgentUUID, receiverAgentUUID string) (string, string, error)
	RecordMessageQueued(orgID string)
	RecordMessageDropped(orgID string)
}

// MessageQueueStore captures enqueue/dequeue behavior for agent messages.
type MessageQueueStore interface {
	Enqueue(ctx context.Context, message model.Message) error
	Dequeue(ctx context.Context, agentUUID string) (model.Message, bool, error)
}
