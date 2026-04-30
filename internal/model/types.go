package model

import "time"

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
	RoleViewer = "viewer"

	StatusPending = "pending"
	StatusActive  = "active"
	StatusBlocked = "blocked"
	StatusRevoked = "revoked"
	StatusExpired = "expired"
	StatusDeleted = "deleted"

	OrgAccessScopeListHumans = "list_humans"
	OrgAccessScopeListAgents = "list_agents"

	MessageDeliveryQueued = "queued"
	MessageDeliveryLeased = "leased"
	MessageDeliveryAcked  = "acked"
	MessageDeliveryFailed = "failed"
	MessageForwarded      = "forwarded"

	AgentMetadataKeyType       = "agent_type"
	AgentMetadataKeySkills     = "skills"
	AgentMetadataKeyProfile    = "profile_markdown"
	AgentMetadataKeyActivities = "activities"
	AgentMetadataKeyHireMe     = "hire_me"
	AgentMetadataKeyPresence   = "presence"
	HumanMetadataKeyPresence   = "presence"
	// AgentMetadataKeySystemActivityLog is internal server-managed activity history.
	// It is append-only and ignored for client-provided metadata updates.
	AgentMetadataKeySystemActivityLog = "_system_activity_log"
	AgentTypeUnknown                  = "unknown"
)

type Organization struct {
	OrgID       string         `json:"org_id"`
	Handle      string         `json:"handle"`
	DisplayName string         `json:"display_name"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   time.Time      `json:"created_at"`
	CreatedBy   string         `json:"created_by"`
}

type Human struct {
	HumanID           string         `json:"human_id"`
	Handle            string         `json:"handle"`
	HandleConfirmedAt *time.Time     `json:"handle_confirmed_at,omitempty"`
	AuthProvider      string         `json:"auth_provider"`
	AuthSubject       string         `json:"auth_subject"`
	Email             string         `json:"email"`
	EmailVerified     bool           `json:"email_verified"`
	Metadata          map[string]any `json:"metadata"`
	CreatedAt         time.Time      `json:"created_at"`
}

type Membership struct {
	MembershipID string    `json:"membership_id"`
	OrgID        string    `json:"org_id"`
	HumanID      string    `json:"human_id"`
	Role         string    `json:"role"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type Invite struct {
	InviteID     string     `json:"invite_id"`
	OrgID        string     `json:"org_id"`
	Email        string     `json:"email"`
	Role         string     `json:"role"`
	Status       string     `json:"status"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	AcceptedAt   *time.Time `json:"accepted_at,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	InviteSecret string     `json:"-"`
}

type Agent struct {
	AgentUUID         string         `json:"agent_uuid"`
	AgentID           string         `json:"agent_id"` // URI metadata: org/agent or org/human/agent
	Handle            string         `json:"handle"`
	HandleFinalizedAt *time.Time     `json:"handle_finalized_at,omitempty"`
	OrgID             string         `json:"org_id"`
	OwnerHumanID      *string        `json:"owner_human_id,omitempty"`
	HostAgentUUID     string         `json:"host_agent_uuid,omitempty"`
	TokenHash         string         `json:"-"`
	Status            string         `json:"status"`
	Metadata          map[string]any `json:"metadata"`
	CreatedBy         string         `json:"created_by"`
	CreatedAt         time.Time      `json:"created_at"`
	RevokedAt         *time.Time     `json:"revoked_at,omitempty"`
}

type PeerInstance struct {
	PeerID            string     `json:"peer_id"`
	CanonicalBaseURL  string     `json:"canonical_base_url"`
	DeliveryBaseURL   string     `json:"delivery_base_url"`
	SharedSecret      string     `json:"-"`
	Status            string     `json:"status"`
	CreatedBy         string     `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastSuccessfulAt  *time.Time `json:"last_successful_at,omitempty"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
	LastFailureReason string     `json:"last_failure_reason,omitempty"`
}

type RemoteOrgTrust struct {
	TrustID         string    `json:"trust_id"`
	LocalOrgID      string    `json:"local_org_id"`
	PeerID          string    `json:"peer_id"`
	RemoteOrgHandle string    `json:"remote_org_handle"`
	Status          string    `json:"status"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RemoteAgentTrust struct {
	TrustID        string    `json:"trust_id"`
	LocalAgentUUID string    `json:"local_agent_uuid"`
	PeerID         string    `json:"peer_id"`
	RemoteAgentURI string    `json:"remote_agent_uri"`
	Status         string    `json:"status"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type BindToken struct {
	BindID        string     `json:"bind_id"`
	OrgID         string     `json:"org_id"`
	OwnerHumanID  *string    `json:"owner_human_id,omitempty"`
	HostAgentUUID string     `json:"host_agent_uuid,omitempty"`
	TokenHash     string     `json:"-"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	UsedAt        *time.Time `json:"used_at,omitempty"`
}

type TrustEdge struct {
	EdgeID        string    `json:"edge_id"`
	EdgeType      string    `json:"edge_type"` // org | agent
	LeftID        string    `json:"left_id"`
	RightID       string    `json:"right_id"`
	State         string    `json:"state"`
	LeftApproved  bool      `json:"left_approved"`
	RightApproved bool      `json:"right_approved"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Message struct {
	MessageID      string    `json:"message_id"`
	FromAgentUUID  string    `json:"from_agent_uuid"`
	ToAgentUUID    string    `json:"to_agent_uuid"`
	FromAgentID    string    `json:"from_agent_id,omitempty"`
	ToAgentID      string    `json:"to_agent_id,omitempty"`
	FromAgentURI   string    `json:"from_agent_uri,omitempty"`
	ToAgentURI     string    `json:"to_agent_uri,omitempty"`
	SenderOrgID    string    `json:"sender_org_id"`
	ReceiverOrgID  string    `json:"receiver_org_id"`
	ReceiverPeerID string    `json:"receiver_peer_id,omitempty"`
	ContentType    string    `json:"content_type"`
	Payload        string    `json:"payload"`
	ClientMsgID    *string   `json:"client_msg_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type MessageRecord struct {
	Message           Message    `json:"message"`
	Status            string     `json:"status"`
	AcceptedAt        time.Time  `json:"accepted_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	FirstReceivedAt   *time.Time `json:"first_received_at,omitempty"`
	LastLeasedAt      *time.Time `json:"last_leased_at,omitempty"`
	LeaseExpiresAt    *time.Time `json:"lease_expires_at,omitempty"`
	AckedAt           *time.Time `json:"acked_at,omitempty"`
	LastDeliveryID    *string    `json:"last_delivery_id,omitempty"`
	DeliveryAttempts  int        `json:"delivery_attempts"`
	RequeueCount      int        `json:"requeue_count"`
	IdempotentReplays int        `json:"idempotent_replays"`
	LastFailureReason string     `json:"last_failure_reason,omitempty"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
}

type MessageDelivery struct {
	DeliveryID     string    `json:"delivery_id"`
	MessageID      string    `json:"message_id"`
	AgentUUID      string    `json:"agent_uuid"`
	Attempt        int       `json:"attempt"`
	LeasedAt       time.Time `json:"leased_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
}

type QueueMetrics struct {
	AvailableMessages   int        `json:"available_messages"`
	LeasedMessages      int        `json:"leased_messages"`
	OldestQueuedAt      *time.Time `json:"oldest_queued_at,omitempty"`
	OldestLeaseExpiryAt *time.Time `json:"oldest_lease_expires_at,omitempty"`
}

type PeerOutboundMessage struct {
	OutboundID      string     `json:"outbound_id"`
	PeerID          string     `json:"peer_id"`
	MessageID       string     `json:"message_id"`
	Message         Message    `json:"message"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	AttemptCount    int        `json:"attempt_count"`
	NextAttemptAt   time.Time  `json:"next_attempt_at"`
	LastAttemptAt   *time.Time `json:"last_attempt_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	LastDeliveredAt *time.Time `json:"last_delivered_at,omitempty"`
}

type OrgHumanView struct {
	HumanID      string         `json:"human_id"`
	Handle       string         `json:"handle"`
	Email        string         `json:"email"`
	Role         string         `json:"role"`
	Status       string         `json:"status"`
	AuthProvider string         `json:"auth_provider"`
	Metadata     map[string]any `json:"metadata"`
}

type OrgStats struct {
	OrgID               string          `json:"org_id"`
	QueuedMessages      int64           `json:"queued_messages"`
	DroppedMessages     int64           `json:"dropped_messages"`
	AckedMessages       int64           `json:"acked_messages"`
	ExpiredMessages     int64           `json:"expired_messages"`
	RedeliveredMessages int64           `json:"redelivered_messages"`
	DuplicateMessages   int64           `json:"duplicate_messages"`
	Last7Days           []OrgDailyStats `json:"last_7_days"`
}

type OrgDailyStats struct {
	Date                string `json:"date"`
	QueuedMessages      int64  `json:"queued_messages"`
	DroppedMessages     int64  `json:"dropped_messages"`
	AckedMessages       int64  `json:"acked_messages"`
	ExpiredMessages     int64  `json:"expired_messages"`
	RedeliveredMessages int64  `json:"redelivered_messages"`
	DuplicateMessages   int64  `json:"duplicate_messages"`
}

type AuditEvent struct {
	EventID    string         `json:"event_id"`
	OrgID      string         `json:"org_id"`
	ActorHuman string         `json:"actor_human_id"`
	Category   string         `json:"category"`
	Action     string         `json:"action"`
	SubjectID  string         `json:"subject_id"`
	Details    map[string]any `json:"details,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

type MembershipWithOrg struct {
	Membership Membership   `json:"membership"`
	Org        Organization `json:"org"`
}

type InviteWithOrg struct {
	Invite Invite       `json:"invite"`
	Org    Organization `json:"org"`
}

type OrgAccessKey struct {
	KeyID      string     `json:"key_id"`
	OrgID      string     `json:"org_id"`
	Label      string     `json:"label"`
	Scopes     []string   `json:"scopes"`
	Status     string     `json:"status"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	TokenHash  string     `json:"-"`
}

type AdminSnapshot struct {
	Organizations         []Organization      `json:"organizations"`
	Humans                []Human             `json:"humans"`
	Memberships           []Membership        `json:"memberships"`
	Agents                []Agent             `json:"agents"`
	ArchivedOrganizations []Organization      `json:"archived_organizations,omitempty"`
	ArchivedHumans        []Human             `json:"archived_humans,omitempty"`
	ArchivedAgents        []Agent             `json:"archived_agents,omitempty"`
	OrgTrusts             []TrustEdge         `json:"org_trusts"`
	AgentTrusts           []TrustEdge         `json:"agent_trusts"`
	Stats                 []OrgStats          `json:"stats"`
	MessageMetrics        AdminMessageMetrics `json:"message_metrics"`
	ActivityFeed          []AuditEvent        `json:"activity_feed"`
}

type MessageArchiveEntry struct {
	MessageID             string     `json:"message_id"`
	CounterpartyAgentUUID string     `json:"counterparty_agent_uuid,omitempty"`
	CounterpartyAgentID   string     `json:"counterparty_agent_id,omitempty"`
	CounterpartyAgentURI  string     `json:"counterparty_agent_uri,omitempty"`
	CounterpartyOrgID     string     `json:"counterparty_org_id,omitempty"`
	ContentType           string     `json:"content_type"`
	PublishedAt           time.Time  `json:"published_at"`
	FirstReceivedAt       *time.Time `json:"first_received_at,omitempty"`
	Status                string     `json:"status"`
}

type AgentMessageArchive struct {
	From []MessageArchiveEntry `json:"from"`
	To   []MessageArchiveEntry `json:"to"`
}

type AgentMessageMetrics struct {
	AgentUUID      string              `json:"agent_uuid"`
	AgentID        string              `json:"agent_id"`
	OrgID          string              `json:"org_id"`
	OwnerHumanID   *string             `json:"owner_human_id,omitempty"`
	AgentType      string              `json:"agent_type"`
	OutboxMessages int64               `json:"outbox_messages"`
	InboxMessages  int64               `json:"inbox_messages"`
	Archive        AgentMessageArchive `json:"archive"`
}

type AgentTypeMessageRollup struct {
	AgentType      string `json:"agent_type"`
	AgentCount     int64  `json:"agent_count"`
	OutboxMessages int64  `json:"outbox_messages"`
	InboxMessages  int64  `json:"inbox_messages"`
}

type HumanMessageMetrics struct {
	HumanID         string                   `json:"human_id"`
	LinkedAgents    int64                    `json:"linked_agents"`
	OutboxMessages  int64                    `json:"outbox_messages"`
	InboxMessages   int64                    `json:"inbox_messages"`
	AgentTypeTotals []AgentTypeMessageRollup `json:"agent_type_totals"`
}

type OrganizationMessageMetrics struct {
	OrgID           string                   `json:"org_id"`
	LinkedAgents    int64                    `json:"linked_agents"`
	OutboxMessages  int64                    `json:"outbox_messages"`
	InboxMessages   int64                    `json:"inbox_messages"`
	AgentTypeTotals []AgentTypeMessageRollup `json:"agent_type_totals"`
}

type AdminMessageMetrics struct {
	Agents        []AgentMessageMetrics        `json:"agents"`
	Humans        []HumanMessageMetrics        `json:"humans"`
	Organizations []OrganizationMessageMetrics `json:"organizations"`
}
