package model

import "time"

// Agent is a registered identity and its server-side auth state.
type Agent struct {
	AgentID   string    `json:"agent_id"`
	TokenHash string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

// Message is a queued message envelope used by the local POC.
type Message struct {
	MessageID   string    `json:"message_id"`
	FromAgentID string    `json:"from_agent_id"`
	ToAgentID   string    `json:"to_agent_id"`
	ContentType string    `json:"content_type"`
	Payload     string    `json:"payload"`
	ClientMsgID *string   `json:"client_msg_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Bond is a bilateral communication relationship between two agents.
type Bond struct {
	BondID      string     `json:"bond_id"`
	AgentAID    string     `json:"agent_a_id"`
	AgentBID    string     `json:"agent_b_id"`
	State       string     `json:"state"` // pending | active
	CreatedAt   time.Time  `json:"created_at"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}
