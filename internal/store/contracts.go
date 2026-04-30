package store

import (
	"context"
	"time"

	"moltenhub/internal/model"
)

// ControlPlaneStore captures all non-queue state operations used by handlers.
type ControlPlaneStore interface {
	UpsertHuman(provider, subject, email string, emailVerified bool, now time.Time, idFactory func() (string, error)) (model.Human, error)
	UpdateHumanProfile(humanID, handle string, confirmHandle bool, now time.Time) (model.Human, error)
	UpdateHumanMetadata(humanID string, metadata map[string]any, now time.Time) (model.Human, error)
	SetHumanPresence(humanID string, presence map[string]any, now time.Time) (model.Human, bool, error)
	GetHumanPresence(humanID string) (map[string]any, bool, error)
	CreateOrg(handle, displayName string, creatorHumanID string, orgID string, now time.Time) (model.Organization, model.Membership, error)
	DeleteOrg(orgID, actorHumanID string, isSuperAdmin bool, now time.Time) error
	EnsurePersonalOrg(humanID string, now time.Time, idFactory func() (string, error)) (model.Organization, error)
	UpdateOrgMetadata(orgID string, metadata map[string]any, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Organization, error)
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
	ListOrgHumanAgents(orgID, humanID, requesterHumanID string, isSuperAdmin bool) ([]model.Agent, error)
	RegisterAgent(orgID, agentID string, ownerHumanID *string, tokenHash, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error)
	CreateBindToken(orgID string, ownerHumanID *string, actorHumanID, bindID, bindTokenHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.BindToken, error)
	CreateAgentInviteBindToken(hostAgentUUID, bindID, bindTokenHash string, expiresAt, now time.Time) (model.BindToken, error)
	RedeemBindToken(bindTokenHash, agentID, agentTokenHash string, now time.Time) (model.Agent, error)
	RotateAgentToken(agentUUID, actorHumanID, tokenHash string, now time.Time, isSuperAdmin bool) error
	RevokeAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error
	DeleteAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error
	UpdateAgentMetadata(agentUUID string, metadata map[string]any, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error)
	UpdateAgentMetadataSelf(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error)
	SetAgentPresence(agentUUID string, presence map[string]any, now time.Time) (model.Agent, bool, error)
	GetAgentPresence(agentUUID string) (map[string]any, bool, error)
	FinalizeAgentHandleSelf(agentUUID, handle string, now time.Time) (model.Agent, error)
	AgentUUIDForTokenHash(tokenHash string) (string, error)
	GetHuman(humanID string) (model.Human, error)
	GetOrganization(orgID string) (model.Organization, error)
	GetAgentByUUID(agentUUID string) (model.Agent, error)
	GetAgentURI(agentUUID string) (string, error)
	ResolveAgentUUID(agentRef string) (string, error)
	ResolveAgentUUIDByURI(agentURI string) (string, error)
	CountActiveHumanOwnedAgents(humanID string) int
	PeekBindToken(bindTokenHash string) (model.BindToken, error)
	ListTalkablePeers(agentUUID string) ([]string, error)
	ListRemoteAgentTrustsForLocalAgent(agentUUID string) ([]model.RemoteAgentTrust, error)
	CreatePeerInstance(canonicalBaseURL, deliveryBaseURL, sharedSecret, actorHumanID, peerID string, now time.Time) (model.PeerInstance, error)
	ListPeerInstances() []model.PeerInstance
	GetPeerInstance(peerID string) (model.PeerInstance, error)
	ResolvePeerByCanonicalBase(canonicalBaseURL string) (model.PeerInstance, error)
	DeletePeerInstance(peerID, actorHumanID string, now time.Time) (model.PeerInstance, error)
	RecordPeerDeliverySuccess(peerID string, now time.Time)
	RecordPeerDeliveryFailure(peerID, reason string, now time.Time)
	CreateRemoteOrgTrust(localOrgID, peerID, remoteOrgHandle, actorHumanID, trustID string, now time.Time) (model.RemoteOrgTrust, error)
	ListRemoteOrgTrusts() []model.RemoteOrgTrust
	DeleteRemoteOrgTrust(trustID, actorHumanID string, now time.Time) (model.RemoteOrgTrust, error)
	HasActiveRemoteOrgTrust(localOrgID, peerID, remoteOrgHandle string) bool
	CreateRemoteAgentTrust(localAgentUUID, peerID, remoteAgentURI, actorHumanID, trustID string, now time.Time) (model.RemoteAgentTrust, error)
	ListRemoteAgentTrusts() []model.RemoteAgentTrust
	DeleteRemoteAgentTrust(trustID, actorHumanID string, now time.Time) (model.RemoteAgentTrust, error)
	HasActiveRemoteAgentTrust(localAgentUUID, peerID, remoteAgentURI string) bool
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
	CreateOrGetMessageRecord(message model.Message, acceptedAt time.Time) (model.MessageRecord, bool, error)
	AbortMessageRecord(messageID string) error
	GetMessageRecord(messageID string) (model.MessageRecord, error)
	ListMessageRecordsForAgent(agentUUID string) ([]model.MessageRecord, error)
	LeaseMessage(messageID, receiverAgentUUID, deliveryID string, leasedAt, leaseExpiresAt time.Time) (model.MessageDelivery, model.MessageRecord, error)
	AckMessageDelivery(receiverAgentUUID, deliveryID string, ackedAt time.Time) (model.MessageRecord, error)
	ReleaseMessageDelivery(receiverAgentUUID, deliveryID string, now time.Time, reason string) (model.Message, model.MessageRecord, error)
	ExpireMessageLeases(now time.Time) ([]model.Message, error)
	GetQueueMetrics() model.QueueMetrics
	RecordAgentSystemActivity(agentUUID string, entry map[string]any, now time.Time) (model.Agent, error)
	RecordMessageQueued(orgID string)
	RecordMessageDropped(orgID string)
	EnqueuePeerOutbound(peerID, outboundID string, message model.Message, now time.Time) (model.PeerOutboundMessage, error)
	ListDuePeerOutbounds(now time.Time, limit int) []model.PeerOutboundMessage
	MarkPeerOutboundRetry(outboundID, reason string, nextAttemptAt, now time.Time) (model.PeerOutboundMessage, error)
	MarkPeerOutboundDelivered(outboundID string, now time.Time) (model.PeerOutboundMessage, error)
	MarkMessageForwarded(messageID string, forwardedAt time.Time) (model.MessageRecord, error)
}

// MessageQueueStore captures enqueue/dequeue behavior for agent messages.
type MessageQueueStore interface {
	Enqueue(ctx context.Context, message model.Message) error
	Dequeue(ctx context.Context, agentUUID string) (model.Message, bool, error)
}
