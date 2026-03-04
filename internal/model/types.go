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

	OrgAccessScopeListHumans = "list_humans"
	OrgAccessScopeListAgents = "list_agents"
)

type Organization struct {
	OrgID     string    `json:"org_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
}

type Human struct {
	HumanID       string    `json:"human_id"`
	AuthProvider  string    `json:"auth_provider"`
	AuthSubject   string    `json:"auth_subject"`
	Email         string    `json:"email"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
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
	InviteID   string     `json:"invite_id"`
	OrgID      string     `json:"org_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	Status     string     `json:"status"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}

type Agent struct {
	AgentID      string     `json:"agent_id"`
	OrgID        string     `json:"org_id"`
	OwnerHumanID *string    `json:"owner_human_id,omitempty"`
	TokenHash    string     `json:"-"`
	Status       string     `json:"status"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

type BindToken struct {
	BindID       string     `json:"bind_id"`
	OrgID        string     `json:"org_id"`
	OwnerHumanID *string    `json:"owner_human_id,omitempty"`
	TokenHash    string     `json:"-"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
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
	MessageID     string    `json:"message_id"`
	FromAgentID   string    `json:"from_agent_id"`
	ToAgentID     string    `json:"to_agent_id"`
	SenderOrgID   string    `json:"sender_org_id"`
	ReceiverOrgID string    `json:"receiver_org_id"`
	ContentType   string    `json:"content_type"`
	Payload       string    `json:"payload"`
	ClientMsgID   *string   `json:"client_msg_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type OrgHumanView struct {
	HumanID      string `json:"human_id"`
	Email        string `json:"email"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	AuthProvider string `json:"auth_provider"`
}

type OrgStats struct {
	OrgID           string          `json:"org_id"`
	QueuedMessages  int64           `json:"queued_messages"`
	DroppedMessages int64           `json:"dropped_messages"`
	Last7Days       []OrgDailyStats `json:"last_7_days"`
}

type OrgDailyStats struct {
	Date            string `json:"date"`
	QueuedMessages  int64  `json:"queued_messages"`
	DroppedMessages int64  `json:"dropped_messages"`
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
	Organizations []Organization `json:"organizations"`
	Humans        []Human        `json:"humans"`
	Memberships   []Membership   `json:"memberships"`
	Agents        []Agent        `json:"agents"`
	OrgTrusts     []TrustEdge    `json:"org_trusts"`
	AgentTrusts   []TrustEdge    `json:"agent_trusts"`
	Stats         []OrgStats     `json:"stats"`
}
