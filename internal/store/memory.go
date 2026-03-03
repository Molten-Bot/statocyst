package store

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"statocyst/internal/model"
)

var (
	ErrAgentExists      = errors.New("agent already exists")
	ErrAgentNotFound    = errors.New("agent not found")
	ErrSenderUnknown    = errors.New("sender agent not found")
	ErrPeerUnknown      = errors.New("peer agent not found")
	ErrSelfBond         = errors.New("cannot create bond with self")
	ErrBondNotFound     = errors.New("bond not found")
	ErrBondAccessDenied = errors.New("bond access denied")
	ErrNoActiveBond     = errors.New("no active bond")
	ErrInvalidToken     = errors.New("invalid token")
)

type bondRecord struct {
	bond         model.Bond
	agentAJoined bool
	agentBJoined bool
}

type MemoryStore struct {
	mu sync.RWMutex

	agents     map[string]model.Agent
	tokenIndex map[string]string
	queues     map[string][]model.Message
	bonds      map[string]bondRecord
	bondByPair map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		agents:     make(map[string]model.Agent),
		tokenIndex: make(map[string]string),
		queues:     make(map[string][]model.Message),
		bonds:      make(map[string]bondRecord),
		bondByPair: make(map[string]string),
	}
}

func (s *MemoryStore) RegisterAgent(agentID, tokenHash string, now time.Time) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.agents[agentID]; exists {
		return model.Agent{}, ErrAgentExists
	}

	agent := model.Agent{
		AgentID:   agentID,
		TokenHash: tokenHash,
		CreatedAt: now,
	}
	s.agents[agentID] = agent
	s.tokenIndex[tokenHash] = agentID
	s.queues[agentID] = s.queues[agentID]

	return agent, nil
}

func (s *MemoryStore) AgentIDForTokenHash(tokenHash string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agentID, ok := s.tokenIndex[tokenHash]
	if !ok {
		return "", ErrInvalidToken
	}
	return agentID, nil
}

func (s *MemoryStore) CreateOrJoinBond(callerAgentID, peerAgentID, bondID string, now time.Time) (model.Bond, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.agents[callerAgentID]; !ok {
		return model.Bond{}, false, ErrAgentNotFound
	}
	if _, ok := s.agents[peerAgentID]; !ok {
		return model.Bond{}, false, ErrPeerUnknown
	}
	if callerAgentID == peerAgentID {
		return model.Bond{}, false, ErrSelfBond
	}

	agentAID, agentBID := canonicalPair(callerAgentID, peerAgentID)
	key := pairKey(agentAID, agentBID)
	if existingBondID, ok := s.bondByPair[key]; ok {
		record := s.bonds[existingBondID]
		if callerAgentID == record.bond.AgentAID {
			record.agentAJoined = true
		}
		if callerAgentID == record.bond.AgentBID {
			record.agentBJoined = true
		}
		if record.agentAJoined && record.agentBJoined && record.bond.State != "active" {
			activatedAt := now
			record.bond.State = "active"
			record.bond.ActivatedAt = &activatedAt
		}
		s.bonds[existingBondID] = record
		return record.bond, false, nil
	}

	record := bondRecord{
		bond: model.Bond{
			BondID:    bondID,
			AgentAID:  agentAID,
			AgentBID:  agentBID,
			State:     "pending",
			CreatedAt: now,
		},
	}
	if callerAgentID == agentAID {
		record.agentAJoined = true
	} else {
		record.agentBJoined = true
	}

	s.bonds[bondID] = record
	s.bondByPair[key] = bondID
	return record.bond, true, nil
}

func (s *MemoryStore) DeleteBond(callerAgentID, bondID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.bonds[bondID]
	if !ok {
		return ErrBondNotFound
	}
	if callerAgentID != record.bond.AgentAID && callerAgentID != record.bond.AgentBID {
		return ErrBondAccessDenied
	}
	delete(s.bonds, bondID)
	delete(s.bondByPair, pairKey(record.bond.AgentAID, record.bond.AgentBID))
	return nil
}

func (s *MemoryStore) CanPublish(senderAgentID, receiverAgentID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.agents[receiverAgentID]; !ok {
		return ErrAgentNotFound
	}
	if _, ok := s.agents[senderAgentID]; !ok {
		return ErrSenderUnknown
	}

	agentAID, agentBID := canonicalPair(senderAgentID, receiverAgentID)
	bondID, ok := s.bondByPair[pairKey(agentAID, agentBID)]
	if !ok {
		return ErrNoActiveBond
	}
	record := s.bonds[bondID]
	if record.bond.State != "active" {
		return ErrNoActiveBond
	}
	return nil
}

func (s *MemoryStore) Enqueue(message model.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.agents[message.ToAgentID]; !ok {
		return ErrAgentNotFound
	}

	s.queues[message.ToAgentID] = append(s.queues[message.ToAgentID], message)
	return nil
}

func (s *MemoryStore) PopNext(agentID string) (model.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	queue := s.queues[agentID]
	if len(queue) == 0 {
		return model.Message{}, false
	}

	message := queue[0]
	s.queues[agentID] = queue[1:]
	return message, true
}

func canonicalPair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

func pairKey(a, b string) string {
	return fmt.Sprintf("%s\x00%s", a, b)
}
