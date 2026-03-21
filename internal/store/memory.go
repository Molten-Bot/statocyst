package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"statocyst/internal/handles"
	"statocyst/internal/model"
)

var (
	ErrInvalidToken             = errors.New("invalid token")
	ErrOrgNotFound              = errors.New("organization not found")
	ErrOrgNameTaken             = errors.New("organization name already exists")
	ErrOrgHandleTaken           = errors.New("organization handle already exists")
	ErrHumanNotFound            = errors.New("human not found")
	ErrHumanHandleTaken         = errors.New("human handle already exists")
	ErrInvalidHandle            = errors.New("invalid handle")
	ErrMembershipNotFound       = errors.New("membership not found")
	ErrInviteNotFound           = errors.New("invite not found")
	ErrInviteInvalid            = errors.New("invite invalid")
	ErrInviteExists             = errors.New("invite already exists")
	ErrOrgAccessKeyNotFound     = errors.New("org access key not found")
	ErrOrgAccessKeyInvalid      = errors.New("org access key invalid")
	ErrOrgAccessScopeDenied     = errors.New("org access scope denied")
	ErrCannotRevokeOwner        = errors.New("cannot revoke owner membership")
	ErrAgentExists              = errors.New("agent already exists")
	ErrAgentNotFound            = errors.New("agent not found")
	ErrAgentAmbiguous           = errors.New("agent reference is ambiguous")
	ErrAgentHandleLocked        = errors.New("agent handle is already finalized")
	ErrAgentRevoked             = errors.New("agent revoked")
	ErrInvalidAgentType         = errors.New("invalid agent type")
	ErrInvalidAgentSkills       = errors.New("invalid agent skills metadata")
	ErrInvalidSkillDescription  = errors.New("invalid agent skill description")
	ErrTrustNotFound            = errors.New("trust edge not found")
	ErrUnauthorizedRole         = errors.New("unauthorized role")
	ErrInvalidRole              = errors.New("invalid role")
	ErrInvalidEdgeType          = errors.New("invalid edge type")
	ErrSelfTrust                = errors.New("self trust not allowed")
	ErrNoTrustPath              = errors.New("no trust path")
	ErrBindNotFound             = errors.New("bind token not found")
	ErrBindExpired              = errors.New("bind token expired")
	ErrBindUsed                 = errors.New("bind token already used")
	ErrAgentLimitExceeded       = errors.New("agent limit exceeded")
	ErrMessageNotFound          = errors.New("message not found")
	ErrMessageDeliveryNotFound  = errors.New("message delivery not found")
	ErrMessageDeliveryMismatch  = errors.New("message delivery does not belong to agent")
	ErrPeerInstanceNotFound     = errors.New("peer instance not found")
	ErrPeerInstanceExists       = errors.New("peer instance already exists")
	ErrRemoteOrgTrustNotFound   = errors.New("remote org trust not found")
	ErrRemoteAgentTrustNotFound = errors.New("remote agent trust not found")
)

type MemoryStore struct {
	mu sync.RWMutex

	orgs        map[string]model.Organization
	orgByHandle map[string]string
	// personalOrgByHuman stores one auto-provisioned personal org per human.
	personalOrgByHuman map[string]string

	humans         map[string]model.Human
	humanByAuthKey map[string]string
	humanByHandle  map[string]string

	memberships         map[string]model.Membership
	membershipByOrgUser map[string]string

	invites            map[string]model.Invite
	inviteBySecretHash map[string]string
	orgAccessKeys      map[string]model.OrgAccessKey
	orgAccessKeyByHash map[string]string

	agents               map[string]model.Agent // key: agent_uuid
	agentByURI           map[string]string      // uri -> agent_uuid
	agentTokenIdx        map[string]string      // token hash -> agent_uuid
	orgOwnedAgentNameIdx map[string]string
	// humanOwnedAgentNameIdx enforces unique human-owned agent names within org+human scope.
	humanOwnedAgentNameIdx map[string]string
	queues                 map[string][]model.Message
	messageRecords         map[string]model.MessageRecord
	messageByClientMsg     map[string]string
	messageDeliveries      map[string]model.MessageDelivery

	binds      map[string]model.BindToken
	bindByHash map[string]string

	orgTrusts      map[string]model.TrustEdge
	orgTrustByPair map[string]string

	agentTrusts      map[string]model.TrustEdge
	agentTrustByPair map[string]string

	peerInstances         map[string]model.PeerInstance
	peerByCanonicalBase   map[string]string
	remoteOrgTrusts       map[string]model.RemoteOrgTrust
	remoteOrgTrustByKey   map[string]string
	remoteAgentTrusts     map[string]model.RemoteAgentTrust
	remoteAgentTrustByKey map[string]string
	peerOutbounds         map[string]model.PeerOutboundMessage
	peerOutboundByPeer    map[string][]string

	auditByOrg map[string][]model.AuditEvent
	statsByOrg map[string]model.OrgStats
	statsDaily map[string]map[string]model.OrgDailyStats
}

var _ ControlPlaneStore = (*MemoryStore)(nil)
var _ MessageQueueStore = (*MemoryStore)(nil)

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		orgs:                   make(map[string]model.Organization),
		orgByHandle:            make(map[string]string),
		personalOrgByHuman:     make(map[string]string),
		humans:                 make(map[string]model.Human),
		humanByAuthKey:         make(map[string]string),
		humanByHandle:          make(map[string]string),
		memberships:            make(map[string]model.Membership),
		membershipByOrgUser:    make(map[string]string),
		invites:                make(map[string]model.Invite),
		inviteBySecretHash:     make(map[string]string),
		orgAccessKeys:          make(map[string]model.OrgAccessKey),
		orgAccessKeyByHash:     make(map[string]string),
		agents:                 make(map[string]model.Agent),
		agentByURI:             make(map[string]string),
		agentTokenIdx:          make(map[string]string),
		orgOwnedAgentNameIdx:   make(map[string]string),
		humanOwnedAgentNameIdx: make(map[string]string),
		queues:                 make(map[string][]model.Message),
		messageRecords:         make(map[string]model.MessageRecord),
		messageByClientMsg:     make(map[string]string),
		messageDeliveries:      make(map[string]model.MessageDelivery),
		binds:                  make(map[string]model.BindToken),
		bindByHash:             make(map[string]string),
		orgTrusts:              make(map[string]model.TrustEdge),
		orgTrustByPair:         make(map[string]string),
		agentTrusts:            make(map[string]model.TrustEdge),
		agentTrustByPair:       make(map[string]string),
		peerInstances:          make(map[string]model.PeerInstance),
		peerByCanonicalBase:    make(map[string]string),
		remoteOrgTrusts:        make(map[string]model.RemoteOrgTrust),
		remoteOrgTrustByKey:    make(map[string]string),
		remoteAgentTrusts:      make(map[string]model.RemoteAgentTrust),
		remoteAgentTrustByKey:  make(map[string]string),
		peerOutbounds:          make(map[string]model.PeerOutboundMessage),
		peerOutboundByPeer:     make(map[string][]string),
		auditByOrg:             make(map[string][]model.AuditEvent),
		statsByOrg:             make(map[string]model.OrgStats),
		statsDaily:             make(map[string]map[string]model.OrgDailyStats),
	}
}

func (s *MemoryStore) UpsertHuman(provider, subject, email string, emailVerified bool, now time.Time, idFactory func() (string, error)) (model.Human, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := authKey(provider, subject)
	if humanID, ok := s.humanByAuthKey[key]; ok {
		h := s.humans[humanID]
		if h.Metadata == nil {
			h.Metadata = map[string]any{}
		}
		if email != "" && !strings.EqualFold(h.Email, email) {
			h.Email = email
		}
		h.EmailVerified = emailVerified
		if h.Handle == "" {
			h.Handle = s.claimUniqueHumanHandleLocked(normalizeHumanHandleCandidate(subject), humanID)
		}
		s.humans[humanID] = h
		s.humanByHandle[h.Handle] = h.HumanID
		return h, nil
	}

	humanID, err := idFactory()
	if err != nil {
		return model.Human{}, err
	}
	h := model.Human{
		HumanID:       humanID,
		Handle:        s.claimUniqueHumanHandleLocked(normalizeHumanHandleCandidate(subject), ""),
		AuthProvider:  provider,
		AuthSubject:   subject,
		Email:         email,
		EmailVerified: emailVerified,
		Metadata:      map[string]any{},
		CreatedAt:     now,
	}
	s.humans[humanID] = h
	s.humanByAuthKey[key] = humanID
	s.humanByHandle[h.Handle] = h.HumanID
	return h, nil
}

func (s *MemoryStore) UpdateHumanProfile(humanID, handle string, confirmHandle bool, now time.Time) (model.Human, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.humans[humanID]
	if !ok {
		return model.Human{}, ErrHumanNotFound
	}
	if h.Metadata == nil {
		h.Metadata = map[string]any{}
	}
	if handle != "" {
		nextHandle := normalizeHumanHandleCandidate(handle)
		if err := handles.ValidateHandle(nextHandle); err != nil {
			return model.Human{}, ErrInvalidHandle
		}
		if ownerID, exists := s.humanByHandle[nextHandle]; exists && ownerID != humanID {
			return model.Human{}, ErrHumanHandleTaken
		}
		if h.Handle != "" && h.Handle != nextHandle {
			delete(s.humanByHandle, h.Handle)
		}
		h.Handle = nextHandle
		s.humanByHandle[h.Handle] = humanID
	}
	if confirmHandle {
		confirmedAt := now
		h.HandleConfirmedAt = &confirmedAt
	}
	s.humans[humanID] = h
	return h, nil
}

func (s *MemoryStore) UpdateHumanMetadata(humanID string, metadata map[string]any, now time.Time) (model.Human, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.humans[humanID]
	if !ok {
		return model.Human{}, ErrHumanNotFound
	}
	h.Metadata = copyMetadata(metadata)
	s.humans[humanID] = h
	return h, nil
}

func (s *MemoryStore) CreateOrg(handle, displayName string, creatorHumanID string, orgID string, now time.Time) (model.Organization, model.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.humans[creatorHumanID]; !ok {
		return model.Organization{}, model.Membership{}, ErrHumanNotFound
	}

	handleKey := normalizeOrgHandleKey(handle)
	if err := handles.ValidateHandle(handleKey); err != nil {
		return model.Organization{}, model.Membership{}, ErrInvalidHandle
	}
	if existingOrgID, ok := s.orgByHandle[handleKey]; ok && existingOrgID != "" {
		return model.Organization{}, model.Membership{}, ErrOrgHandleTaken
	}

	if strings.TrimSpace(displayName) == "" {
		displayName = handleKey
	}

	org := model.Organization{
		OrgID:       orgID,
		Handle:      handleKey,
		DisplayName: strings.TrimSpace(displayName),
		Metadata:    map[string]any{},
		CreatedAt:   now,
		CreatedBy:   creatorHumanID,
	}
	s.orgs[org.OrgID] = org
	s.orgByHandle[handleKey] = org.OrgID

	memID := fmt.Sprintf("m-%s", orgID)
	mem := model.Membership{
		MembershipID: memID,
		OrgID:        org.OrgID,
		HumanID:      creatorHumanID,
		Role:         model.RoleOwner,
		Status:       model.StatusActive,
		CreatedAt:    now,
	}
	s.memberships[memID] = mem
	s.membershipByOrgUser[orgHumanKey(org.OrgID, creatorHumanID)] = memID
	s.ensureOrgStatsLocked(org.OrgID)
	s.appendAuditLocked(org.OrgID, creatorHumanID, "org", "create", org.OrgID, nil, now)
	return org, mem, nil
}

func (s *MemoryStore) DeleteOrg(orgID, actorHumanID string, isSuperAdmin bool, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = now

	org, ok := s.orgs[orgID]
	if !ok {
		return ErrOrgNotFound
	}
	if !isSuperAdmin && s.membershipRoleLocked(orgID, actorHumanID) != model.RoleOwner {
		return ErrUnauthorizedRole
	}

	deletedAgents := make(map[string]struct{})
	for agentUUID, agent := range s.agents {
		if agent.OrgID != orgID {
			continue
		}
		if agent.OwnerHumanID != nil {
			delete(s.humanOwnedAgentNameIdx, humanOwnedAgentNameKey(agent.OrgID, *agent.OwnerHumanID, agent.Handle))
		} else {
			delete(s.orgOwnedAgentNameIdx, orgOwnedAgentNameKey(agent.OrgID, agent.Handle))
		}
		delete(s.agentTokenIdx, agent.TokenHash)
		delete(s.agentByURI, agent.AgentID)
		delete(s.agents, agentUUID)
		deletedAgents[agentUUID] = struct{}{}
	}

	for edgeID, edge := range s.agentTrusts {
		if _, leftDeleted := deletedAgents[edge.LeftID]; !leftDeleted {
			if _, rightDeleted := deletedAgents[edge.RightID]; !rightDeleted {
				continue
			}
		}
		delete(s.agentTrusts, edgeID)
		delete(s.agentTrustByPair, pairKey(edge.LeftID, edge.RightID))
	}
	for edgeID, edge := range s.orgTrusts {
		if edge.LeftID != orgID && edge.RightID != orgID {
			continue
		}
		delete(s.orgTrusts, edgeID)
		delete(s.orgTrustByPair, pairKey(edge.LeftID, edge.RightID))
	}

	for queueAgentUUID, queue := range s.queues {
		if _, removed := deletedAgents[queueAgentUUID]; removed {
			delete(s.queues, queueAgentUUID)
			continue
		}
		if len(queue) == 0 {
			continue
		}
		filtered := queue[:0]
		for _, msg := range queue {
			_, fromDeleted := deletedAgents[msg.FromAgentUUID]
			_, toDeleted := deletedAgents[msg.ToAgentUUID]
			if fromDeleted || toDeleted {
				continue
			}
			filtered = append(filtered, msg)
		}
		if len(filtered) == 0 {
			delete(s.queues, queueAgentUUID)
			continue
		}
		s.queues[queueAgentUUID] = filtered
	}

	for bindID, bind := range s.binds {
		if bind.OrgID != orgID {
			continue
		}
		delete(s.binds, bindID)
		delete(s.bindByHash, bind.TokenHash)
	}
	for inviteID, invite := range s.invites {
		if invite.OrgID != orgID {
			continue
		}
		delete(s.invites, inviteID)
		if invite.InviteSecret != "" {
			delete(s.inviteBySecretHash, invite.InviteSecret)
		}
	}
	for membershipID, membership := range s.memberships {
		if membership.OrgID != orgID {
			continue
		}
		delete(s.memberships, membershipID)
		delete(s.membershipByOrgUser, orgHumanKey(orgID, membership.HumanID))
	}
	for keyID, key := range s.orgAccessKeys {
		if key.OrgID != orgID {
			continue
		}
		delete(s.orgAccessKeys, keyID)
		delete(s.orgAccessKeyByHash, key.TokenHash)
	}
	for humanID, personalOrgID := range s.personalOrgByHuman {
		if personalOrgID == orgID {
			delete(s.personalOrgByHuman, humanID)
		}
	}

	delete(s.auditByOrg, orgID)
	delete(s.statsByOrg, orgID)
	delete(s.statsDaily, orgID)
	delete(s.orgByHandle, normalizeOrgHandleKey(org.Handle))
	delete(s.orgs, orgID)
	return nil
}

func (s *MemoryStore) EnsurePersonalOrg(humanID string, now time.Time, idFactory func() (string, error)) (model.Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensurePersonalOrgLocked(humanID, now, idFactory)
}

func (s *MemoryStore) ensurePersonalOrgLocked(humanID string, now time.Time, idFactory func() (string, error)) (model.Organization, error) {
	if _, ok := s.humans[humanID]; !ok {
		return model.Organization{}, ErrHumanNotFound
	}
	if orgID, ok := s.personalOrgByHuman[humanID]; ok {
		if org, found := s.orgs[orgID]; found {
			return org, nil
		}
	}

	baseHandle := normalizeOrgHandleKey(fmt.Sprintf("personal-%s", s.humans[humanID].Handle))
	if strings.TrimSpace(baseHandle) == "" || baseHandle == "personal-" {
		baseHandle = normalizeOrgHandleKey(fmt.Sprintf("personal-%s", humanID))
	}
	if err := handles.ValidateHandle(baseHandle); err != nil {
		baseHandle = "personal-" + humanID
		baseHandle = normalizeOrgHandleKey(baseHandle)
	}
	handle := baseHandle
	for i := 2; ; i++ {
		handleKey := normalizeOrgHandleKey(handle)
		if existingOrgID, exists := s.orgByHandle[handleKey]; !exists || existingOrgID == "" {
			break
		}
		handle = fmt.Sprintf("%s-%d", baseHandle, i)
	}

	orgID, err := idFactory()
	if err != nil {
		return model.Organization{}, err
	}
	org := model.Organization{
		OrgID:       orgID,
		Handle:      handle,
		DisplayName: fmt.Sprintf("Personal %s", s.humans[humanID].Handle),
		Metadata:    map[string]any{},
		CreatedAt:   now,
		CreatedBy:   humanID,
	}
	s.orgs[org.OrgID] = org
	s.orgByHandle[normalizeOrgHandleKey(handle)] = org.OrgID
	s.personalOrgByHuman[humanID] = org.OrgID

	memID := fmt.Sprintf("m-%s", orgID)
	mem := model.Membership{
		MembershipID: memID,
		OrgID:        org.OrgID,
		HumanID:      humanID,
		Role:         model.RoleOwner,
		Status:       model.StatusActive,
		CreatedAt:    now,
	}
	s.memberships[memID] = mem
	s.membershipByOrgUser[orgHumanKey(org.OrgID, humanID)] = memID
	s.ensureOrgStatsLocked(org.OrgID)
	s.appendAuditLocked(org.OrgID, humanID, "org", "create", org.OrgID, map[string]any{
		"mode": "auto_personal",
	}, now)
	return org, nil
}

func (s *MemoryStore) ListMyMemberships(humanID string) []model.MembershipWithOrg {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.MembershipWithOrg, 0)
	for _, m := range s.memberships {
		if m.HumanID != humanID || m.Status != model.StatusActive {
			continue
		}
		org, ok := s.orgs[m.OrgID]
		if !ok {
			continue
		}
		out = append(out, model.MembershipWithOrg{Membership: m, Org: org})
	}
	return out
}

func (s *MemoryStore) CreateInvite(orgID, email, role, actorHumanID, inviteID, inviteSecretHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[orgID]; !ok {
		return model.Invite{}, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleAdmin) {
		return model.Invite{}, ErrUnauthorizedRole
	}
	if !isValidRole(role) || role == model.RoleOwner {
		return model.Invite{}, ErrInvalidRole
	}
	if inviteSecretHash == "" {
		return model.Invite{}, ErrInviteInvalid
	}
	if _, exists := s.inviteBySecretHash[inviteSecretHash]; exists {
		return model.Invite{}, ErrInviteInvalid
	}
	if !expiresAt.After(now) {
		return model.Invite{}, ErrInviteInvalid
	}
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for _, inv := range s.invites {
		if inv.OrgID != orgID || !strings.EqualFold(inv.Email, normalizedEmail) {
			continue
		}
		if deriveInviteStatus(inv, now).Status == model.StatusPending {
			return model.Invite{}, ErrInviteExists
		}
	}
	for _, membership := range s.memberships {
		if membership.OrgID != orgID || membership.Status != model.StatusActive {
			continue
		}
		human, ok := s.humans[membership.HumanID]
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(human.Email), normalizedEmail) {
			return model.Invite{}, ErrInviteExists
		}
	}

	invite := model.Invite{
		InviteID:     inviteID,
		OrgID:        orgID,
		Email:        normalizedEmail,
		Role:         role,
		Status:       model.StatusPending,
		CreatedBy:    actorHumanID,
		CreatedAt:    now,
		ExpiresAt:    expiresAt,
		InviteSecret: inviteSecretHash,
	}
	s.invites[invite.InviteID] = invite
	s.inviteBySecretHash[inviteSecretHash] = invite.InviteID
	s.appendAuditLocked(orgID, actorHumanID, "invite", "create", invite.InviteID, map[string]any{
		"email":      invite.Email,
		"role":       invite.Role,
		"expires_at": invite.ExpiresAt,
	}, now)
	return invite, nil
}

func (s *MemoryStore) AcceptInvite(inviteID, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.acceptInviteLocked(inviteID, humanID, humanEmail, now, idFactory)
}

func (s *MemoryStore) AcceptInviteBySecretHash(inviteSecretHash, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	inviteID, ok := s.inviteBySecretHash[inviteSecretHash]
	if !ok || inviteID == "" {
		return model.Membership{}, ErrInviteNotFound
	}
	return s.acceptInviteLocked(inviteID, humanID, humanEmail, now, idFactory)
}

func (s *MemoryStore) acceptInviteLocked(inviteID, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error) {
	invite, ok := s.invites[inviteID]
	if !ok {
		return model.Membership{}, ErrInviteNotFound
	}
	if invite.Status == model.StatusPending && now.After(invite.ExpiresAt) {
		invite.Status = model.StatusExpired
		expiredAt := now
		invite.RevokedAt = &expiredAt
		s.invites[inviteID] = invite
		delete(s.inviteBySecretHash, invite.InviteSecret)
		return model.Membership{}, ErrInviteInvalid
	}
	if invite.Status != model.StatusPending {
		return model.Membership{}, ErrInviteInvalid
	}
	if invite.Email != "" && !strings.EqualFold(invite.Email, humanEmail) {
		return model.Membership{}, ErrInviteInvalid
	}
	if _, ok := s.humans[humanID]; !ok {
		return model.Membership{}, ErrHumanNotFound
	}

	if existingID, ok := s.membershipByOrgUser[orgHumanKey(invite.OrgID, humanID)]; ok {
		m := s.memberships[existingID]
		if m.Status == model.StatusActive {
			accepted := now
			invite.Status = model.StatusActive
			invite.AcceptedAt = &accepted
			s.invites[inviteID] = invite
			delete(s.inviteBySecretHash, invite.InviteSecret)
			s.transferHumanOwnedAgentsToOrgLocked(invite.OrgID, humanID, now)
			return m, nil
		}
	}

	memID, err := idFactory()
	if err != nil {
		return model.Membership{}, err
	}
	mem := model.Membership{
		MembershipID: memID,
		OrgID:        invite.OrgID,
		HumanID:      humanID,
		Role:         invite.Role,
		Status:       model.StatusActive,
		CreatedAt:    now,
	}
	s.memberships[memID] = mem
	s.membershipByOrgUser[orgHumanKey(invite.OrgID, humanID)] = memID

	accepted := now
	invite.Status = model.StatusActive
	invite.AcceptedAt = &accepted
	s.invites[inviteID] = invite
	delete(s.inviteBySecretHash, invite.InviteSecret)
	s.transferHumanOwnedAgentsToOrgLocked(invite.OrgID, humanID, now)
	s.appendAuditLocked(invite.OrgID, humanID, "invite", "accept", inviteID, nil, now)
	return mem, nil
}

func (s *MemoryStore) transferHumanOwnedAgentsToOrgLocked(orgID, humanID string, now time.Time) {
	orgID = strings.TrimSpace(orgID)
	humanID = strings.TrimSpace(humanID)
	if orgID == "" || humanID == "" {
		return
	}
	org, ok := s.orgs[orgID]
	if !ok {
		return
	}
	human, ok := s.humans[humanID]
	if !ok {
		return
	}
	if err := handles.ValidateHandle(human.Handle); err != nil {
		return
	}
	ownerHandle := human.Handle

	agentIDs := make([]string, 0)
	for agentUUID, agent := range s.agents {
		if agent.Status == model.StatusRevoked {
			continue
		}
		if agent.OrgID != "" || agent.OwnerHumanID == nil || *agent.OwnerHumanID != humanID {
			continue
		}
		agentIDs = append(agentIDs, agentUUID)
	}
	sort.Strings(agentIDs)

	for _, agentUUID := range agentIDs {
		agent, ok := s.agents[agentUUID]
		if !ok {
			continue
		}
		oldAgentID := agent.AgentID
		nextAgentID := handles.BuildAgentURI(org.Handle, &ownerHandle, agent.Handle)
		nextNameKey := humanOwnedAgentNameKey(orgID, humanID, agent.Handle)
		if existingAgentUUID, exists := s.humanOwnedAgentNameIdx[nextNameKey]; exists && existingAgentUUID != agentUUID {
			continue
		}
		if existingAgentUUID, exists := s.agentByURI[nextAgentID]; exists && existingAgentUUID != agentUUID {
			continue
		}

		delete(s.humanOwnedAgentNameIdx, humanOwnedAgentNameKey("", humanID, agent.Handle))
		delete(s.agentByURI, oldAgentID)

		agent.OrgID = orgID
		agent.AgentID = nextAgentID
		s.agents[agentUUID] = agent
		s.humanOwnedAgentNameIdx[nextNameKey] = agentUUID
		s.agentByURI[nextAgentID] = agentUUID

		s.appendAuditLocked(orgID, humanID, "agent", "transfer_on_invite_accept", agentUUID, map[string]any{
			"old_org_id":   "",
			"new_org_id":   orgID,
			"old_agent_id": oldAgentID,
			"new_agent_id": nextAgentID,
		}, now)
	}
}

func (s *MemoryStore) ListInvitesForHuman(humanID, humanEmail string, isSuperAdmin bool) []model.InviteWithOrg {
	s.mu.RLock()
	defer s.mu.RUnlock()

	email := strings.ToLower(strings.TrimSpace(humanEmail))
	out := make([]model.InviteWithOrg, 0)
	now := time.Now().UTC()
	for _, inv := range s.invites {
		if !isSuperAdmin && email != "" && !strings.EqualFold(inv.Email, email) {
			continue
		}
		org, ok := s.orgs[inv.OrgID]
		if !ok {
			continue
		}
		inv = deriveInviteStatus(inv, now)
		out = append(out, model.InviteWithOrg{
			Invite: inv,
			Org:    org,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Invite.CreatedAt.After(out[j].Invite.CreatedAt)
	})
	return out
}

func (s *MemoryStore) ListOrgInvites(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.Invite, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, requesterHumanID), model.RoleAdmin) {
		return nil, ErrUnauthorizedRole
	}

	now := time.Now().UTC()
	out := make([]model.Invite, 0)
	for _, inv := range s.invites {
		if inv.OrgID != orgID {
			continue
		}
		out = append(out, deriveInviteStatus(inv, now))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) RevokeInvite(inviteID, actorHumanID, actorEmail string, isSuperAdmin bool, now time.Time) (model.Invite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	invite, ok := s.invites[inviteID]
	if !ok {
		return model.Invite{}, ErrInviteNotFound
	}

	allowed := strings.EqualFold(invite.Email, strings.TrimSpace(actorEmail))
	if !allowed {
		role := s.membershipRoleLocked(invite.OrgID, actorHumanID)
		allowed = hasRoleAtLeast(role, model.RoleAdmin)
	}
	if isSuperAdmin {
		allowed = true
	}
	if !allowed {
		return model.Invite{}, ErrUnauthorizedRole
	}

	if invite.Status == model.StatusRevoked {
		return invite, nil
	}
	delete(s.inviteBySecretHash, invite.InviteSecret)
	invite.Status = model.StatusRevoked
	revokedAt := now
	invite.RevokedAt = &revokedAt
	s.invites[inviteID] = invite
	s.appendAuditLocked(invite.OrgID, actorHumanID, "invite", "revoke", inviteID, nil, now)
	return invite, nil
}

func (s *MemoryStore) RevokeMembership(orgID, humanID, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[orgID]; !ok {
		return model.Membership{}, ErrOrgNotFound
	}
	targetMembershipID, ok := s.membershipByOrgUser[orgHumanKey(orgID, humanID)]
	if !ok {
		return model.Membership{}, ErrMembershipNotFound
	}
	targetMembership, ok := s.memberships[targetMembershipID]
	if !ok || targetMembership.Status != model.StatusActive {
		return model.Membership{}, ErrMembershipNotFound
	}

	actorRole := s.membershipRoleLocked(orgID, actorHumanID)
	if !isSuperAdmin && !hasRoleAtLeast(actorRole, model.RoleAdmin) {
		return model.Membership{}, ErrUnauthorizedRole
	}

	if targetMembership.Role == model.RoleOwner {
		return model.Membership{}, ErrCannotRevokeOwner
	}
	if !isSuperAdmin && actorRole == model.RoleAdmin && targetMembership.Role == model.RoleAdmin {
		return model.Membership{}, ErrUnauthorizedRole
	}

	targetMembership.Status = model.StatusRevoked
	s.memberships[targetMembershipID] = targetMembership
	s.appendAuditLocked(orgID, actorHumanID, "membership", "revoke", humanID, map[string]any{
		"role": targetMembership.Role,
	}, now)
	return targetMembership, nil
}

func (s *MemoryStore) CreateOrgAccessKey(
	orgID, label string,
	scopes []string,
	expiresAt *time.Time,
	actorHumanID, keyID, tokenHash string,
	now time.Time,
	isSuperAdmin bool,
) (model.OrgAccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[orgID]; !ok {
		return model.OrgAccessKey{}, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleAdmin) {
		return model.OrgAccessKey{}, ErrUnauthorizedRole
	}
	if _, exists := s.orgAccessKeyByHash[tokenHash]; exists {
		return model.OrgAccessKey{}, ErrOrgAccessKeyInvalid
	}

	normalizedScopes := normalizeOrgAccessScopes(scopes)
	if len(normalizedScopes) == 0 {
		return model.OrgAccessKey{}, ErrOrgAccessScopeDenied
	}
	if strings.TrimSpace(label) == "" {
		label = "Organization Access Key"
	}

	key := model.OrgAccessKey{
		KeyID:      keyID,
		OrgID:      orgID,
		Label:      strings.TrimSpace(label),
		Scopes:     normalizedScopes,
		Status:     model.StatusActive,
		CreatedBy:  actorHumanID,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
		LastUsedAt: nil,
		RevokedAt:  nil,
		TokenHash:  tokenHash,
	}
	s.orgAccessKeys[keyID] = key
	s.orgAccessKeyByHash[tokenHash] = keyID
	s.appendAuditLocked(orgID, actorHumanID, "org_access_key", "create", keyID, map[string]any{
		"label":      key.Label,
		"scopes":     key.Scopes,
		"expires_at": key.ExpiresAt,
	}, now)
	return key, nil
}

func (s *MemoryStore) ListOrgAccessKeys(orgID, actorHumanID string, isSuperAdmin bool) ([]model.OrgAccessKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleAdmin) {
		return nil, ErrUnauthorizedRole
	}

	out := make([]model.OrgAccessKey, 0)
	for _, key := range s.orgAccessKeys {
		if key.OrgID != orgID {
			continue
		}
		key.Scopes = append([]string(nil), key.Scopes...)
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) RevokeOrgAccessKey(orgID, keyID, actorHumanID string, isSuperAdmin bool, now time.Time) (model.OrgAccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[orgID]; !ok {
		return model.OrgAccessKey{}, ErrOrgNotFound
	}
	key, ok := s.orgAccessKeys[keyID]
	if !ok || key.OrgID != orgID {
		return model.OrgAccessKey{}, ErrOrgAccessKeyNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleAdmin) {
		return model.OrgAccessKey{}, ErrUnauthorizedRole
	}
	if key.Status == model.StatusRevoked {
		return key, nil
	}

	delete(s.orgAccessKeyByHash, key.TokenHash)
	key.Status = model.StatusRevoked
	revoked := now
	key.RevokedAt = &revoked
	s.orgAccessKeys[keyID] = key
	s.appendAuditLocked(orgID, actorHumanID, "org_access_key", "revoke", keyID, nil, now)
	return key, nil
}

func (s *MemoryStore) AuthorizeOrgAccessByName(orgName, accessKeyHash, requiredScope string, now time.Time) (model.Organization, model.OrgAccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	orgID, ok := s.orgByHandle[normalizeOrgHandleKey(orgName)]
	if !ok || orgID == "" {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgNotFound
	}
	org, ok := s.orgs[orgID]
	if !ok {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgNotFound
	}

	keyID, ok := s.orgAccessKeyByHash[accessKeyHash]
	if !ok || keyID == "" {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgAccessKeyNotFound
	}
	key, ok := s.orgAccessKeys[keyID]
	if !ok || key.OrgID != orgID {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgAccessKeyNotFound
	}
	if key.Status != model.StatusActive || key.RevokedAt != nil {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgAccessKeyInvalid
	}
	if key.ExpiresAt != nil && now.After(*key.ExpiresAt) {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgAccessKeyInvalid
	}
	if !orgAccessScopeAllowed(key.Scopes, requiredScope) {
		return model.Organization{}, model.OrgAccessKey{}, ErrOrgAccessScopeDenied
	}

	usedAt := now
	key.LastUsedAt = &usedAt
	s.orgAccessKeys[keyID] = key
	key.Scopes = append([]string(nil), key.Scopes...)
	return org, key, nil
}

func (s *MemoryStore) ListOrgHumans(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.OrgHumanView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	if !isSuperAdmin && s.membershipRoleLocked(orgID, requesterHumanID) == "" {
		return nil, ErrUnauthorizedRole
	}

	out := make([]model.OrgHumanView, 0)
	for _, m := range s.memberships {
		if m.OrgID != orgID || m.Status != model.StatusActive {
			continue
		}
		h, ok := s.humans[m.HumanID]
		if !ok {
			continue
		}
		out = append(out, model.OrgHumanView{
			HumanID:      h.HumanID,
			Handle:       h.Handle,
			Email:        h.Email,
			Role:         m.Role,
			Status:       m.Status,
			AuthProvider: h.AuthProvider,
			Metadata:     copyMetadata(h.Metadata),
		})
	}
	return out, nil
}

func (s *MemoryStore) RegisterAgent(orgID, agentID string, ownerHumanID *string, tokenHash, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agentHandle := normalizeAgentNameKey(agentID)
	if err := handles.ValidateHandle(agentHandle); err != nil {
		return model.Agent{}, ErrInvalidHandle
	}

	var ownerHandle *string
	if ownerHumanID != nil {
		if orgID != "" && s.membershipRoleLocked(orgID, *ownerHumanID) == "" {
			return model.Agent{}, ErrMembershipNotFound
		}
		if orgID == "" && !isSuperAdmin && actorHumanID != *ownerHumanID {
			return model.Agent{}, ErrUnauthorizedRole
		}
		human, ok := s.humans[*ownerHumanID]
		if !ok {
			return model.Agent{}, ErrHumanNotFound
		}
		if err := handles.ValidateHandle(human.Handle); err != nil {
			return model.Agent{}, ErrInvalidHandle
		}
		oh := human.Handle
		ownerHandle = &oh
		key := humanOwnedAgentNameKey(orgID, *ownerHumanID, agentHandle)
		if _, exists := s.humanOwnedAgentNameIdx[key]; exists {
			return model.Agent{}, ErrAgentExists
		}
	} else {
		if orgID == "" {
			return model.Agent{}, ErrMembershipNotFound
		}
		key := orgOwnedAgentNameKey(orgID, agentHandle)
		if _, exists := s.orgOwnedAgentNameIdx[key]; exists {
			return model.Agent{}, ErrAgentExists
		}
	}

	var agentURI string
	if orgID == "" {
		if ownerHandle == nil {
			return model.Agent{}, ErrMembershipNotFound
		}
		agentURI = handles.BuildHumanAgentURI(*ownerHandle, agentHandle)
	} else {
		org, ok := s.orgs[orgID]
		if !ok {
			return model.Agent{}, ErrOrgNotFound
		}
		if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleMember) {
			return model.Agent{}, ErrUnauthorizedRole
		}
		agentURI = handles.BuildAgentURI(org.Handle, ownerHandle, agentHandle)
	}
	if _, exists := s.agentByURI[agentURI]; exists {
		return model.Agent{}, ErrAgentExists
	}
	agentUUID, err := newRandomUUID()
	if err != nil {
		return model.Agent{}, err
	}

	agent := model.Agent{
		AgentUUID:         agentUUID,
		AgentID:           agentURI,
		Handle:            agentHandle,
		HandleFinalizedAt: &now,
		OrgID:             orgID,
		OwnerHumanID:      ownerHumanID,
		TokenHash:         tokenHash,
		Status:            model.StatusActive,
		Metadata:          defaultAgentMetadata(),
		CreatedBy:         actorHumanID,
		CreatedAt:         now,
	}
	s.agents[agentUUID] = agent
	s.agentByURI[agentURI] = agentUUID
	s.agentTokenIdx[tokenHash] = agentUUID
	if ownerHumanID != nil {
		s.humanOwnedAgentNameIdx[humanOwnedAgentNameKey(orgID, *ownerHumanID, agentHandle)] = agentUUID
	} else {
		s.orgOwnedAgentNameIdx[orgOwnedAgentNameKey(orgID, agentHandle)] = agentUUID
	}
	s.queues[agentUUID] = s.queues[agentUUID]
	if orgID != "" {
		s.appendAuditLocked(orgID, actorHumanID, "agent", "register", agent.AgentUUID, map[string]any{
			"agent_id":       agent.AgentID,
			"agent_uuid":     agent.AgentUUID,
			"owner_human_id": ownerHumanID,
			"handle":         agentHandle,
		}, now)
	}
	return agent, nil
}

func (s *MemoryStore) CreateBindToken(orgID string, ownerHumanID *string, actorHumanID, bindID, bindTokenHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.BindToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if orgID != "" {
		if _, ok := s.orgs[orgID]; !ok {
			return model.BindToken{}, ErrOrgNotFound
		}
		if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleMember) {
			return model.BindToken{}, ErrUnauthorizedRole
		}
		if ownerHumanID != nil && s.membershipRoleLocked(orgID, *ownerHumanID) == "" {
			return model.BindToken{}, ErrMembershipNotFound
		}
	} else {
		if ownerHumanID == nil || strings.TrimSpace(*ownerHumanID) == "" {
			return model.BindToken{}, ErrMembershipNotFound
		}
		if _, ok := s.humans[*ownerHumanID]; !ok {
			return model.BindToken{}, ErrMembershipNotFound
		}
		if !isSuperAdmin && actorHumanID != *ownerHumanID {
			return model.BindToken{}, ErrUnauthorizedRole
		}
	}
	if _, exists := s.bindByHash[bindTokenHash]; exists {
		return model.BindToken{}, ErrInvalidToken
	}

	bind := model.BindToken{
		BindID:       bindID,
		OrgID:        orgID,
		OwnerHumanID: ownerHumanID,
		TokenHash:    bindTokenHash,
		CreatedBy:    actorHumanID,
		CreatedAt:    now,
		ExpiresAt:    expiresAt,
	}
	s.binds[bind.BindID] = bind
	s.bindByHash[bindTokenHash] = bind.BindID
	if orgID != "" {
		s.appendAuditLocked(orgID, actorHumanID, "agent_bind", "create", bind.BindID, map[string]any{
			"owner_human_id": ownerHumanID,
			"expires_at":     expiresAt,
		}, now)
	}
	return bind, nil
}

func (s *MemoryStore) RedeemBindToken(bindTokenHash, agentID, agentTokenHash string, now time.Time) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bindID, ok := s.bindByHash[bindTokenHash]
	if !ok {
		return model.Agent{}, ErrBindNotFound
	}
	bind, ok := s.binds[bindID]
	if !ok {
		return model.Agent{}, ErrBindNotFound
	}
	if bind.UsedAt != nil {
		return model.Agent{}, ErrBindUsed
	}
	if now.After(bind.ExpiresAt) {
		return model.Agent{}, ErrBindExpired
	}
	agentHandle := normalizeAgentNameKey(agentID)
	if err := handles.ValidateHandle(agentHandle); err != nil {
		return model.Agent{}, ErrInvalidHandle
	}

	var ownerHandle *string
	if bind.OwnerHumanID != nil {
		if bind.OrgID != "" && s.membershipRoleLocked(bind.OrgID, *bind.OwnerHumanID) == "" {
			return model.Agent{}, ErrMembershipNotFound
		}
		human, ok := s.humans[*bind.OwnerHumanID]
		if !ok {
			return model.Agent{}, ErrHumanNotFound
		}
		if err := handles.ValidateHandle(human.Handle); err != nil {
			return model.Agent{}, ErrInvalidHandle
		}
		oh := human.Handle
		ownerHandle = &oh
		if _, exists := s.humanOwnedAgentNameIdx[humanOwnedAgentNameKey(bind.OrgID, *bind.OwnerHumanID, agentHandle)]; exists {
			return model.Agent{}, ErrAgentExists
		}
	} else {
		if bind.OrgID == "" {
			return model.Agent{}, ErrMembershipNotFound
		}
		if _, exists := s.orgOwnedAgentNameIdx[orgOwnedAgentNameKey(bind.OrgID, agentHandle)]; exists {
			return model.Agent{}, ErrAgentExists
		}
	}

	var agentURI string
	if bind.OrgID == "" {
		if ownerHandle == nil {
			return model.Agent{}, ErrMembershipNotFound
		}
		agentURI = handles.BuildHumanAgentURI(*ownerHandle, agentHandle)
	} else {
		org, ok := s.orgs[bind.OrgID]
		if !ok {
			return model.Agent{}, ErrOrgNotFound
		}
		agentURI = handles.BuildAgentURI(org.Handle, ownerHandle, agentHandle)
	}
	if _, exists := s.agentByURI[agentURI]; exists {
		return model.Agent{}, ErrAgentExists
	}
	agentUUID, err := newRandomUUID()
	if err != nil {
		return model.Agent{}, err
	}

	agent := model.Agent{
		AgentUUID:         agentUUID,
		AgentID:           agentURI,
		Handle:            agentHandle,
		HandleFinalizedAt: nil,
		OrgID:             bind.OrgID,
		OwnerHumanID:      bind.OwnerHumanID,
		TokenHash:         agentTokenHash,
		Status:            model.StatusActive,
		Metadata:          defaultAgentMetadata(),
		CreatedBy:         bind.CreatedBy,
		CreatedAt:         now,
	}
	s.agents[agent.AgentUUID] = agent
	s.agentByURI[agent.AgentID] = agent.AgentUUID
	s.agentTokenIdx[agentTokenHash] = agent.AgentUUID
	if bind.OwnerHumanID != nil {
		s.humanOwnedAgentNameIdx[humanOwnedAgentNameKey(bind.OrgID, *bind.OwnerHumanID, agent.Handle)] = agent.AgentUUID
	} else {
		s.orgOwnedAgentNameIdx[orgOwnedAgentNameKey(bind.OrgID, agent.Handle)] = agent.AgentUUID
	}
	s.queues[agent.AgentUUID] = s.queues[agent.AgentUUID]
	used := now
	bind.UsedAt = &used
	s.binds[bind.BindID] = bind
	if bind.OrgID != "" {
		s.appendAuditLocked(bind.OrgID, bind.CreatedBy, "agent_bind", "redeem", bind.BindID, map[string]any{
			"agent_id":   agent.AgentID,
			"agent_uuid": agent.AgentUUID,
		}, now)
	}
	return agent, nil
}

func (s *MemoryStore) RotateAgentToken(agentUUID, actorHumanID, tokenHash string, now time.Time, isSuperAdmin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return ErrAgentNotFound
	}
	if !isSuperAdmin && !s.canManageAgentLocked(agent, actorHumanID) {
		return ErrUnauthorizedRole
	}
	delete(s.agentTokenIdx, agent.TokenHash)
	agent.TokenHash = tokenHash
	s.agents[agentUUID] = agent
	s.agentTokenIdx[tokenHash] = agentUUID
	s.appendAuditLocked(agent.OrgID, actorHumanID, "agent", "rotate_token", agentUUID, map[string]any{
		"agent_id": agent.AgentID,
	}, now)
	return nil
}

func (s *MemoryStore) RevokeAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok {
		return ErrAgentNotFound
	}
	if !isSuperAdmin && !s.canManageAgentLocked(agent, actorHumanID) {
		return ErrUnauthorizedRole
	}
	if agent.Status == model.StatusRevoked {
		return nil
	}
	delete(s.agentTokenIdx, agent.TokenHash)
	if agent.OwnerHumanID != nil {
		delete(s.humanOwnedAgentNameIdx, humanOwnedAgentNameKey(agent.OrgID, *agent.OwnerHumanID, agent.Handle))
	} else {
		delete(s.orgOwnedAgentNameIdx, orgOwnedAgentNameKey(agent.OrgID, agent.Handle))
	}
	delete(s.agentByURI, agent.AgentID)
	agent.Status = model.StatusRevoked
	revokedAt := now
	agent.RevokedAt = &revokedAt
	s.agents[agentUUID] = agent
	revokedTrustEdges := 0
	for edgeID, edge := range s.agentTrusts {
		if edge.LeftID != agentUUID && edge.RightID != agentUUID {
			continue
		}
		if edge.State == model.StatusRevoked {
			continue
		}
		edge.State = model.StatusRevoked
		edge.LeftApproved = false
		edge.RightApproved = false
		edge.UpdatedAt = now
		s.agentTrusts[edgeID] = edge
		revokedTrustEdges++
	}

	purgedMessages := 0
	for queueAgentUUID, queue := range s.queues {
		if len(queue) == 0 {
			continue
		}
		filtered := queue[:0]
		for _, msg := range queue {
			if msg.FromAgentUUID == agentUUID || msg.ToAgentUUID == agentUUID {
				purgedMessages++
				continue
			}
			filtered = append(filtered, msg)
		}
		if len(filtered) == 0 {
			delete(s.queues, queueAgentUUID)
			continue
		}
		s.queues[queueAgentUUID] = filtered
	}
	s.appendAuditLocked(agent.OrgID, actorHumanID, "agent", "revoke", agentUUID, map[string]any{
		"agent_id":                  agent.AgentID,
		"revoked_agent_trust_edges": revokedTrustEdges,
		"purged_queued_messages":    purgedMessages,
	}, now)
	return nil
}

func (s *MemoryStore) DeleteAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok {
		return ErrAgentNotFound
	}
	if !isSuperAdmin && !s.canDeleteAgentLocked(agent, actorHumanID) {
		return ErrUnauthorizedRole
	}

	delete(s.agentTokenIdx, agent.TokenHash)
	if agent.OwnerHumanID != nil {
		delete(s.humanOwnedAgentNameIdx, humanOwnedAgentNameKey(agent.OrgID, *agent.OwnerHumanID, agent.Handle))
	} else {
		delete(s.orgOwnedAgentNameIdx, orgOwnedAgentNameKey(agent.OrgID, agent.Handle))
	}
	delete(s.agentByURI, agent.AgentID)
	delete(s.agents, agentUUID)

	deletedTrustEdges := 0
	for edgeID, edge := range s.agentTrusts {
		if edge.LeftID != agentUUID && edge.RightID != agentUUID {
			continue
		}
		delete(s.agentTrusts, edgeID)
		delete(s.agentTrustByPair, pairKey(edge.LeftID, edge.RightID))
		deletedTrustEdges++
	}

	purgedMessages := 0
	for queueAgentUUID, queue := range s.queues {
		if queueAgentUUID == agentUUID {
			purgedMessages += len(queue)
			delete(s.queues, queueAgentUUID)
			continue
		}
		if len(queue) == 0 {
			continue
		}
		filtered := queue[:0]
		for _, msg := range queue {
			if msg.FromAgentUUID == agentUUID || msg.ToAgentUUID == agentUUID {
				purgedMessages++
				continue
			}
			filtered = append(filtered, msg)
		}
		if len(filtered) == 0 {
			delete(s.queues, queueAgentUUID)
			continue
		}
		s.queues[queueAgentUUID] = filtered
	}

	s.appendAuditLocked(agent.OrgID, actorHumanID, "agent", "delete", agentUUID, map[string]any{
		"agent_id":                agent.AgentID,
		"deleted_agent_trusts":    deletedTrustEdges,
		"purged_queued_messages":  purgedMessages,
		"deleted_previous_status": agent.Status,
	}, now)
	return nil
}

func (s *MemoryStore) UpdateOrgMetadata(orgID string, metadata map[string]any, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	org, ok := s.orgs[orgID]
	if !ok {
		return model.Organization{}, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleAdmin) {
		return model.Organization{}, ErrUnauthorizedRole
	}
	org.Metadata = copyMetadata(metadata)
	s.orgs[orgID] = org
	s.appendAuditLocked(orgID, actorHumanID, "org", "set_metadata", orgID, metadataAuditSummary(metadata), now)
	return org, nil
}

func (s *MemoryStore) UpdateAgentMetadata(agentUUID string, metadata map[string]any, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return model.Agent{}, ErrAgentNotFound
	}
	if !isSuperAdmin && !s.canManageAgentLocked(agent, actorHumanID) {
		return model.Agent{}, ErrUnauthorizedRole
	}
	normalizedMetadata, err := validateAndNormalizeAgentMetadata(metadata)
	if err != nil {
		return model.Agent{}, err
	}
	agent.Metadata = normalizedMetadata
	s.agents[agentUUID] = agent
	summary := metadataAuditSummary(agent.Metadata)
	summary["agent_id"] = agent.AgentID
	s.appendAuditLocked(agent.OrgID, actorHumanID, "agent", "set_metadata", agentUUID, summary, now)
	return agent, nil
}

func (s *MemoryStore) UpdateAgentMetadataSelf(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return model.Agent{}, ErrAgentNotFound
	}
	normalizedMetadata, err := validateAndNormalizeAgentMetadata(metadata)
	if err != nil {
		return model.Agent{}, err
	}
	agent.Metadata = normalizedMetadata
	s.agents[agentUUID] = agent
	summary := metadataAuditSummary(agent.Metadata)
	summary["agent_id"] = agent.AgentID
	s.appendAuditLocked(agent.OrgID, "", "agent", "set_metadata_self", agentUUID, summary, now)
	return agent, nil
}

func (s *MemoryStore) FinalizeAgentHandleSelf(agentUUID, handle string, now time.Time) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return model.Agent{}, ErrAgentNotFound
	}

	nextHandle := normalizeAgentNameKey(handle)
	if err := handles.ValidateHandle(nextHandle); err != nil {
		return model.Agent{}, ErrInvalidHandle
	}

	if agent.HandleFinalizedAt != nil {
		if agent.Handle == nextHandle {
			return agent, nil
		}
		return model.Agent{}, ErrAgentHandleLocked
	}

	var ownerHandle *string
	if agent.OwnerHumanID != nil {
		human, ok := s.humans[*agent.OwnerHumanID]
		if !ok {
			return model.Agent{}, ErrHumanNotFound
		}
		if err := handles.ValidateHandle(human.Handle); err != nil {
			return model.Agent{}, ErrInvalidHandle
		}
		oh := human.Handle
		ownerHandle = &oh
	}

	var nextAgentURI string
	if agent.OrgID == "" {
		if ownerHandle == nil {
			return model.Agent{}, ErrMembershipNotFound
		}
		nextAgentURI = handles.BuildHumanAgentURI(*ownerHandle, nextHandle)
	} else {
		org, ok := s.orgs[agent.OrgID]
		if !ok {
			return model.Agent{}, ErrOrgNotFound
		}
		nextAgentURI = handles.BuildAgentURI(org.Handle, ownerHandle, nextHandle)
	}

	if owner, exists := s.agentByURI[nextAgentURI]; exists && owner != agent.AgentUUID {
		return model.Agent{}, ErrAgentExists
	}
	if agent.OwnerHumanID != nil {
		nextKey := humanOwnedAgentNameKey(agent.OrgID, *agent.OwnerHumanID, nextHandle)
		if owner, exists := s.humanOwnedAgentNameIdx[nextKey]; exists && owner != agent.AgentUUID {
			return model.Agent{}, ErrAgentExists
		}
		oldKey := humanOwnedAgentNameKey(agent.OrgID, *agent.OwnerHumanID, agent.Handle)
		delete(s.humanOwnedAgentNameIdx, oldKey)
		s.humanOwnedAgentNameIdx[nextKey] = agent.AgentUUID
	} else {
		nextKey := orgOwnedAgentNameKey(agent.OrgID, nextHandle)
		if owner, exists := s.orgOwnedAgentNameIdx[nextKey]; exists && owner != agent.AgentUUID {
			return model.Agent{}, ErrAgentExists
		}
		oldKey := orgOwnedAgentNameKey(agent.OrgID, agent.Handle)
		delete(s.orgOwnedAgentNameIdx, oldKey)
		s.orgOwnedAgentNameIdx[nextKey] = agent.AgentUUID
	}

	delete(s.agentByURI, agent.AgentID)
	agent.Handle = nextHandle
	agent.AgentID = nextAgentURI
	finalizedAt := now
	agent.HandleFinalizedAt = &finalizedAt
	s.agentByURI[agent.AgentID] = agent.AgentUUID
	s.agents[agent.AgentUUID] = agent

	summary := map[string]any{
		"agent_id":   agent.AgentID,
		"agent_uuid": agent.AgentUUID,
		"handle":     agent.Handle,
	}
	s.appendAuditLocked(agent.OrgID, "", "agent", "finalize_handle_self", agentUUID, summary, now)
	return agent, nil
}

func metadataAuditSummary(metadata map[string]any) map[string]any {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	summary := map[string]any{
		"metadata_keys": keys,
	}
	if body, err := json.Marshal(metadata); err == nil {
		summary["metadata_size_bytes"] = len(body)
	}
	return summary
}

func copyMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func defaultAgentMetadata() map[string]any {
	return map[string]any{
		model.AgentMetadataKeyType: model.AgentTypeUnknown,
	}
}

func validateAndNormalizeAgentMetadata(metadata map[string]any) (map[string]any, error) {
	normalized := copyMetadata(metadata)
	rawType, hasType := normalized[model.AgentMetadataKeyType]
	if !hasType {
		normalized[model.AgentMetadataKeyType] = model.AgentTypeUnknown
	} else {
		rawTypeValue, ok := rawType.(string)
		if !ok {
			return nil, ErrInvalidAgentType
		}
		agentType, ok := normalizeAgentType(rawTypeValue)
		if !ok {
			return nil, ErrInvalidAgentType
		}
		normalized[model.AgentMetadataKeyType] = agentType
	}

	normalizedSkills, hasSkills, err := normalizeAndValidateAgentSkillsMetadata(normalized, model.AgentMetadataKeySkills)
	if err != nil {
		return nil, err
	}
	if hasSkills {
		normalized[model.AgentMetadataKeySkills] = normalizedSkills
	} else {
		delete(normalized, model.AgentMetadataKeySkills)
	}

	return normalized, nil
}

func normalizeAndValidateAgentSkillsMetadata(metadata map[string]any, key string) ([]map[string]any, bool, error) {
	raw, ok := metadata[key]
	if !ok {
		return nil, false, nil
	}
	if raw == nil {
		return []map[string]any{}, true, nil
	}

	entries := []map[string]any{}
	switch typed := raw.(type) {
	case []map[string]any:
		entries = append(entries, typed...)
	case []any:
		for _, value := range typed {
			obj, ok := value.(map[string]any)
			if !ok {
				return nil, true, ErrInvalidAgentSkills
			}
			entries = append(entries, obj)
		}
	default:
		return nil, true, ErrInvalidAgentSkills
	}

	byName := map[string]string{}
	for _, entry := range entries {
		rawName, _ := entry["name"].(string)
		name, ok := normalizeAgentSkillName(rawName)
		if !ok {
			return nil, true, ErrInvalidAgentSkills
		}
		description, _ := entry["description"].(string)
		description = strings.TrimSpace(description)
		if description == "" || len(description) > 240 {
			return nil, true, ErrInvalidAgentSkills
		}
		if containsLikelySecret(description) {
			return nil, true, ErrInvalidSkillDescription
		}
		byName[name] = description
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	normalized := make([]map[string]any, 0, len(names))
	for _, name := range names {
		normalized = append(normalized, map[string]any{
			"name":        name,
			"description": byName[name],
		})
	}
	return normalized, true, nil
}

func normalizeAgentSkillName(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) < 2 || len(normalized) > 64 {
		return "", false
	}
	for _, ch := range normalized {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.':
		default:
			return "", false
		}
	}
	return normalized, true
}

func containsLikelySecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	secretMarkers := []string{
		"api key",
		"api_key",
		"apikey",
		"access key",
		"secret",
		"password",
		"passwd",
		"private key",
		"bearer ",
		"token=",
		"token:",
	}
	for _, marker := range secretMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func agentTypeFromMetadata(metadata map[string]any) string {
	rawType, ok := metadata[model.AgentMetadataKeyType]
	if !ok {
		return model.AgentTypeUnknown
	}
	rawTypeValue, ok := rawType.(string)
	if !ok {
		return model.AgentTypeUnknown
	}
	return normalizeAgentTypeOrUnknown(rawTypeValue)
}

func normalizeAgentTypeOrUnknown(raw string) string {
	normalized, ok := normalizeAgentType(raw)
	if !ok {
		return model.AgentTypeUnknown
	}
	return normalized
}

func normalizeAgentType(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) < 2 || len(normalized) > 64 {
		return "", false
	}
	for _, ch := range normalized {
		switch {
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '-', ch == '_', ch == '.':
		default:
			return "", false
		}
	}
	return normalized, true
}

func (s *MemoryStore) AgentUUIDForTokenHash(tokenHash string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agentUUID, ok := s.agentTokenIdx[tokenHash]
	if !ok {
		return "", ErrInvalidToken
	}
	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return "", ErrInvalidToken
	}
	return agentUUID, nil
}

func (s *MemoryStore) GetHuman(humanID string) (model.Human, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	h, ok := s.humans[humanID]
	if !ok {
		return model.Human{}, ErrHumanNotFound
	}
	return h, nil
}

func (s *MemoryStore) GetOrganization(orgID string) (model.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	org, ok := s.orgs[orgID]
	if !ok {
		return model.Organization{}, ErrOrgNotFound
	}
	return org, nil
}

func (s *MemoryStore) GetAgentByUUID(agentUUID string) (model.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return model.Agent{}, ErrAgentNotFound
	}
	return agent, nil
}

func (s *MemoryStore) GetAgentURI(agentUUID string) (string, error) {
	agent, err := s.GetAgentByUUID(agentUUID)
	if err != nil {
		return "", err
	}
	return agent.AgentID, nil
}

func (s *MemoryStore) ResolveAgentUUID(agentRef string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolveAgentRefLocked(agentRef)
}

func (s *MemoryStore) ResolveAgentUUIDByURI(agentURI string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ref := agentRefFromURI(agentURI)
	if ref == "" {
		return "", ErrAgentNotFound
	}
	return s.resolveAgentRefLocked(ref)
}

func (s *MemoryStore) CountActiveHumanOwnedAgents(humanID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := 0
	for _, agent := range s.agents {
		if agent.OwnerHumanID == nil {
			continue
		}
		if *agent.OwnerHumanID != humanID {
			continue
		}
		if agent.Status != model.StatusActive {
			continue
		}
		total++
	}
	return total
}

func (s *MemoryStore) PeekBindToken(bindTokenHash string) (model.BindToken, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bindID, ok := s.bindByHash[bindTokenHash]
	if !ok || bindID == "" {
		return model.BindToken{}, ErrBindNotFound
	}
	bind, ok := s.binds[bindID]
	if !ok {
		return model.BindToken{}, ErrBindNotFound
	}
	return bind, nil
}

func (s *MemoryStore) ListTalkablePeers(agentUUID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	agent, ok := s.agents[agentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return nil, ErrAgentNotFound
	}

	out := make([]string, 0)
	for peerUUID, peer := range s.agents {
		if peerUUID == agentUUID || peer.Status == model.StatusRevoked {
			continue
		}
		if s.canPublishLocked(agent, peer) {
			out = append(out, peerUUID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemoryStore) ListRemoteAgentTrustsForLocalAgent(agentUUID string) ([]model.RemoteAgentTrust, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if agent, ok := s.agents[agentUUID]; !ok || agent.Status == model.StatusRevoked {
		return nil, ErrAgentNotFound
	}
	out := make([]model.RemoteAgentTrust, 0)
	for _, trust := range s.remoteAgentTrusts {
		if trust.LocalAgentUUID != agentUUID || trust.Status != model.StatusActive {
			continue
		}
		out = append(out, trust)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RemoteAgentURI == out[j].RemoteAgentURI {
			return out[i].TrustID < out[j].TrustID
		}
		return out[i].RemoteAgentURI < out[j].RemoteAgentURI
	})
	return out, nil
}

func (s *MemoryStore) CreatePeerInstance(canonicalBaseURL, deliveryBaseURL, sharedSecret, actorHumanID, peerID string, now time.Time) (model.PeerInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(canonicalBaseURL) == "" || strings.TrimSpace(deliveryBaseURL) == "" || strings.TrimSpace(sharedSecret) == "" {
		return model.PeerInstance{}, ErrInvalidToken
	}
	if _, exists := s.peerInstances[strings.TrimSpace(peerID)]; exists {
		return model.PeerInstance{}, ErrPeerInstanceExists
	}
	baseKey := normalizeBaseURL(canonicalBaseURL)
	if existingID, ok := s.peerByCanonicalBase[baseKey]; ok {
		existing := s.peerInstances[existingID]
		if existing.Status == model.StatusActive {
			return model.PeerInstance{}, ErrPeerInstanceExists
		}
	}
	peer := model.PeerInstance{
		PeerID:           peerID,
		CanonicalBaseURL: baseKey,
		DeliveryBaseURL:  normalizeBaseURL(deliveryBaseURL),
		SharedSecret:     strings.TrimSpace(sharedSecret),
		Status:           model.StatusActive,
		CreatedBy:        actorHumanID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	s.peerInstances[peer.PeerID] = peer
	s.peerByCanonicalBase[baseKey] = peer.PeerID
	return peer, nil
}

func (s *MemoryStore) ListPeerInstances() []model.PeerInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.PeerInstance, 0, len(s.peerInstances))
	for _, peer := range s.peerInstances {
		out = append(out, peer)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CanonicalBaseURL == out[j].CanonicalBaseURL {
			return out[i].PeerID < out[j].PeerID
		}
		return out[i].CanonicalBaseURL < out[j].CanonicalBaseURL
	})
	return out
}

func (s *MemoryStore) GetPeerInstance(peerID string) (model.PeerInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peer, ok := s.peerInstances[strings.TrimSpace(peerID)]
	if !ok {
		return model.PeerInstance{}, ErrPeerInstanceNotFound
	}
	return peer, nil
}

func (s *MemoryStore) ResolvePeerByCanonicalBase(canonicalBaseURL string) (model.PeerInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	peerID, ok := s.peerByCanonicalBase[normalizeBaseURL(canonicalBaseURL)]
	if !ok {
		return model.PeerInstance{}, ErrPeerInstanceNotFound
	}
	peer, ok := s.peerInstances[peerID]
	if !ok {
		return model.PeerInstance{}, ErrPeerInstanceNotFound
	}
	return peer, nil
}

func (s *MemoryStore) DeletePeerInstance(peerID, actorHumanID string, now time.Time) (model.PeerInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	peer, ok := s.peerInstances[strings.TrimSpace(peerID)]
	if !ok {
		return model.PeerInstance{}, ErrPeerInstanceNotFound
	}
	delete(s.peerInstances, peer.PeerID)
	delete(s.peerByCanonicalBase, peer.CanonicalBaseURL)
	delete(s.peerOutboundByPeer, peer.PeerID)
	for outboundID, outbound := range s.peerOutbounds {
		if outbound.PeerID == peer.PeerID {
			delete(s.peerOutbounds, outboundID)
		}
	}
	for trustID, trust := range s.remoteOrgTrusts {
		if trust.PeerID == peer.PeerID {
			delete(s.remoteOrgTrusts, trustID)
			delete(s.remoteOrgTrustByKey, remoteOrgTrustKey(trust.LocalOrgID, trust.PeerID, trust.RemoteOrgHandle))
		}
	}
	for trustID, trust := range s.remoteAgentTrusts {
		if trust.PeerID == peer.PeerID {
			delete(s.remoteAgentTrusts, trustID)
			delete(s.remoteAgentTrustByKey, remoteAgentTrustKey(trust.LocalAgentUUID, trust.PeerID, trust.RemoteAgentURI))
		}
	}
	peer.Status = model.StatusRevoked
	peer.UpdatedAt = now
	_ = actorHumanID
	return peer, nil
}

func (s *MemoryStore) RecordPeerDeliverySuccess(peerID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	peer, ok := s.peerInstances[strings.TrimSpace(peerID)]
	if !ok {
		return
	}
	peer.LastSuccessfulAt = timePtr(now)
	peer.LastFailureAt = nil
	peer.LastFailureReason = ""
	peer.UpdatedAt = now
	s.peerInstances[peer.PeerID] = peer
}

func (s *MemoryStore) RecordPeerDeliveryFailure(peerID, reason string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	peer, ok := s.peerInstances[strings.TrimSpace(peerID)]
	if !ok {
		return
	}
	peer.LastFailureAt = timePtr(now)
	peer.LastFailureReason = strings.TrimSpace(reason)
	peer.UpdatedAt = now
	s.peerInstances[peer.PeerID] = peer
}

func (s *MemoryStore) CreateRemoteOrgTrust(localOrgID, peerID, remoteOrgHandle, actorHumanID, trustID string, now time.Time) (model.RemoteOrgTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[localOrgID]; !ok {
		return model.RemoteOrgTrust{}, ErrOrgNotFound
	}
	if _, ok := s.peerInstances[peerID]; !ok {
		return model.RemoteOrgTrust{}, ErrPeerInstanceNotFound
	}
	key := remoteOrgTrustKey(localOrgID, peerID, remoteOrgHandle)
	if existingID, ok := s.remoteOrgTrustByKey[key]; ok {
		existing := s.remoteOrgTrusts[existingID]
		existing.Status = model.StatusActive
		existing.UpdatedAt = now
		s.remoteOrgTrusts[existingID] = existing
		return existing, nil
	}
	trust := model.RemoteOrgTrust{
		TrustID:         trustID,
		LocalOrgID:      localOrgID,
		PeerID:          peerID,
		RemoteOrgHandle: handles.Normalize(remoteOrgHandle),
		Status:          model.StatusActive,
		CreatedBy:       actorHumanID,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	s.remoteOrgTrusts[trust.TrustID] = trust
	s.remoteOrgTrustByKey[key] = trust.TrustID
	return trust, nil
}

func (s *MemoryStore) ListRemoteOrgTrusts() []model.RemoteOrgTrust {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.RemoteOrgTrust, 0, len(s.remoteOrgTrusts))
	for _, trust := range s.remoteOrgTrusts {
		out = append(out, trust)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LocalOrgID == out[j].LocalOrgID {
			return out[i].RemoteOrgHandle < out[j].RemoteOrgHandle
		}
		return out[i].LocalOrgID < out[j].LocalOrgID
	})
	return out
}

func (s *MemoryStore) DeleteRemoteOrgTrust(trustID, actorHumanID string, now time.Time) (model.RemoteOrgTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trust, ok := s.remoteOrgTrusts[strings.TrimSpace(trustID)]
	if !ok {
		return model.RemoteOrgTrust{}, ErrRemoteOrgTrustNotFound
	}
	delete(s.remoteOrgTrusts, trust.TrustID)
	delete(s.remoteOrgTrustByKey, remoteOrgTrustKey(trust.LocalOrgID, trust.PeerID, trust.RemoteOrgHandle))
	trust.Status = model.StatusRevoked
	trust.UpdatedAt = now
	_ = actorHumanID
	return trust, nil
}

func (s *MemoryStore) HasActiveRemoteOrgTrust(localOrgID, peerID, remoteOrgHandle string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	trustID, ok := s.remoteOrgTrustByKey[remoteOrgTrustKey(localOrgID, peerID, remoteOrgHandle)]
	if !ok {
		return false
	}
	trust, ok := s.remoteOrgTrusts[trustID]
	return ok && trust.Status == model.StatusActive
}

func (s *MemoryStore) CreateRemoteAgentTrust(localAgentUUID, peerID, remoteAgentURI, actorHumanID, trustID string, now time.Time) (model.RemoteAgentTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if agent, ok := s.agents[localAgentUUID]; !ok || agent.Status == model.StatusRevoked {
		return model.RemoteAgentTrust{}, ErrAgentNotFound
	}
	if _, ok := s.peerInstances[peerID]; !ok {
		return model.RemoteAgentTrust{}, ErrPeerInstanceNotFound
	}
	key := remoteAgentTrustKey(localAgentUUID, peerID, remoteAgentURI)
	if existingID, ok := s.remoteAgentTrustByKey[key]; ok {
		existing := s.remoteAgentTrusts[existingID]
		existing.Status = model.StatusActive
		existing.UpdatedAt = now
		s.remoteAgentTrusts[existingID] = existing
		return existing, nil
	}
	trust := model.RemoteAgentTrust{
		TrustID:        trustID,
		LocalAgentUUID: localAgentUUID,
		PeerID:         peerID,
		RemoteAgentURI: strings.TrimSpace(remoteAgentURI),
		Status:         model.StatusActive,
		CreatedBy:      actorHumanID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	s.remoteAgentTrusts[trust.TrustID] = trust
	s.remoteAgentTrustByKey[key] = trust.TrustID
	return trust, nil
}

func (s *MemoryStore) ListRemoteAgentTrusts() []model.RemoteAgentTrust {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.RemoteAgentTrust, 0, len(s.remoteAgentTrusts))
	for _, trust := range s.remoteAgentTrusts {
		out = append(out, trust)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LocalAgentUUID == out[j].LocalAgentUUID {
			return out[i].RemoteAgentURI < out[j].RemoteAgentURI
		}
		return out[i].LocalAgentUUID < out[j].LocalAgentUUID
	})
	return out
}

func (s *MemoryStore) DeleteRemoteAgentTrust(trustID, actorHumanID string, now time.Time) (model.RemoteAgentTrust, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	trust, ok := s.remoteAgentTrusts[strings.TrimSpace(trustID)]
	if !ok {
		return model.RemoteAgentTrust{}, ErrRemoteAgentTrustNotFound
	}
	delete(s.remoteAgentTrusts, trust.TrustID)
	delete(s.remoteAgentTrustByKey, remoteAgentTrustKey(trust.LocalAgentUUID, trust.PeerID, trust.RemoteAgentURI))
	trust.Status = model.StatusRevoked
	trust.UpdatedAt = now
	_ = actorHumanID
	return trust, nil
}

func (s *MemoryStore) HasActiveRemoteAgentTrust(localAgentUUID, peerID, remoteAgentURI string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	trustID, ok := s.remoteAgentTrustByKey[remoteAgentTrustKey(localAgentUUID, peerID, remoteAgentURI)]
	if !ok {
		return false
	}
	trust, ok := s.remoteAgentTrusts[trustID]
	return ok && trust.Status == model.StatusActive
}

func (s *MemoryStore) CreateOrJoinOrgTrust(orgID, peerOrgID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createOrJoinTrustLocked("org", orgID, peerOrgID, actorHumanID, edgeID, now, isSuperAdmin)
}

func (s *MemoryStore) CreateOrJoinAgentTrust(orgID, agentUUID, peerAgentUUID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.agents[agentUUID]
	if !ok || a.Status == model.StatusRevoked {
		return model.TrustEdge{}, false, ErrAgentNotFound
	}
	if orgID != "" && a.OrgID != orgID {
		return model.TrustEdge{}, false, ErrUnauthorizedRole
	}
	if !isSuperAdmin && !s.canRequestAgentTrustLocked(a, actorHumanID) {
		return model.TrustEdge{}, false, ErrUnauthorizedRole
	}
	peer, ok := s.agents[peerAgentUUID]
	if !ok || peer.Status == model.StatusRevoked {
		return model.TrustEdge{}, false, ErrAgentNotFound
	}
	return s.createOrJoinTrustLocked("agent", agentUUID, peerAgentUUID, actorHumanID, edgeID, now, isSuperAdmin)
}

func (s *MemoryStore) ApproveOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approveTrustLocked("org", edgeID, actorHumanID, now, isSuperAdmin)
}

func (s *MemoryStore) BlockOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockTrustLocked("org", edgeID, actorHumanID, now, isSuperAdmin)
}

func (s *MemoryStore) RevokeOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeTrustLocked("org", edgeID, actorHumanID, now, isSuperAdmin)
}

func (s *MemoryStore) ApproveAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approveTrustLocked("agent", edgeID, actorHumanID, now, isSuperAdmin)
}

func (s *MemoryStore) BlockAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blockTrustLocked("agent", edgeID, actorHumanID, now, isSuperAdmin)
}

func (s *MemoryStore) RevokeAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revokeTrustLocked("agent", edgeID, actorHumanID, now, isSuperAdmin)
}

func (s *MemoryStore) ListOrgAgents(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.Agent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	if !isSuperAdmin && s.membershipRoleLocked(orgID, requesterHumanID) == "" {
		return nil, ErrUnauthorizedRole
	}
	out := make([]model.Agent, 0)
	for _, a := range s.agents {
		if a.OrgID == orgID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListHumanAgents(humanID string) []model.Agent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.Agent, 0)
	for _, agent := range s.agents {
		if s.canManageAgentLocked(agent, humanID) {
			out = append(out, agent)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].AgentID < out[j].AgentID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

func (s *MemoryStore) ListHumanAgentTrusts(humanID string) []model.TrustEdge {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.TrustEdge, 0)
	for _, edge := range s.agentTrusts {
		leftAgent, lok := s.agents[edge.LeftID]
		rightAgent, rok := s.agents[edge.RightID]
		if !lok || !rok {
			continue
		}
		if s.canManageAgentLocked(leftAgent, humanID) || s.canManageAgentLocked(rightAgent, humanID) {
			out = append(out, edge)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].EdgeID < out[j].EdgeID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (s *MemoryStore) ListOrgTrustGraph(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.TrustEdge, []model.TrustEdge, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.orgs[orgID]; !ok {
		return nil, nil, ErrOrgNotFound
	}
	if !isSuperAdmin && s.membershipRoleLocked(orgID, requesterHumanID) == "" {
		return nil, nil, ErrUnauthorizedRole
	}

	orgEdges := make([]model.TrustEdge, 0)
	for _, e := range s.orgTrusts {
		if e.LeftID == orgID || e.RightID == orgID {
			orgEdges = append(orgEdges, e)
		}
	}
	agentEdges := make([]model.TrustEdge, 0)
	for _, e := range s.agentTrusts {
		leftAgent, lok := s.agents[e.LeftID]
		rightAgent, rok := s.agents[e.RightID]
		if !lok || !rok {
			continue
		}
		if leftAgent.OrgID == orgID || rightAgent.OrgID == orgID {
			agentEdges = append(agentEdges, e)
		}
	}
	return orgEdges, agentEdges, nil
}

func (s *MemoryStore) ListAudit(orgID, requesterHumanID string, isSuperAdmin bool) ([]model.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	if !isSuperAdmin && s.membershipRoleLocked(orgID, requesterHumanID) == "" {
		return nil, ErrUnauthorizedRole
	}
	events := s.auditByOrg[orgID]
	out := make([]model.AuditEvent, len(events))
	copy(out, events)
	return out, nil
}

func (s *MemoryStore) GetOrgStats(orgID, requesterHumanID string, isSuperAdmin bool) (model.OrgStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.orgs[orgID]; !ok {
		return model.OrgStats{}, ErrOrgNotFound
	}
	if !isSuperAdmin && s.membershipRoleLocked(orgID, requesterHumanID) == "" {
		return model.OrgStats{}, ErrUnauthorizedRole
	}
	stats := s.statsByOrg[orgID]
	stats.Last7Days = s.last7DaysLocked(orgID, time.Now().UTC())
	return stats, nil
}

func (s *MemoryStore) AdminSnapshot() model.AdminSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := model.AdminSnapshot{
		Organizations: make([]model.Organization, 0, len(s.orgs)),
		Humans:        make([]model.Human, 0, len(s.humans)),
		Memberships:   make([]model.Membership, 0, len(s.memberships)),
		Agents:        make([]model.Agent, 0, len(s.agents)),
		OrgTrusts:     make([]model.TrustEdge, 0, len(s.orgTrusts)),
		AgentTrusts:   make([]model.TrustEdge, 0, len(s.agentTrusts)),
		Stats:         make([]model.OrgStats, 0, len(s.statsByOrg)),
		MessageMetrics: model.AdminMessageMetrics{
			Agents:        make([]model.AgentMessageMetrics, 0, len(s.agents)),
			Humans:        make([]model.HumanMessageMetrics, 0, len(s.humans)),
			Organizations: make([]model.OrganizationMessageMetrics, 0, len(s.orgs)),
		},
		ActivityFeed: make([]model.AuditEvent, 0),
	}

	for _, v := range s.orgs {
		snapshot.Organizations = append(snapshot.Organizations, v)
	}
	for _, v := range s.humans {
		snapshot.Humans = append(snapshot.Humans, v)
	}
	for _, v := range s.memberships {
		snapshot.Memberships = append(snapshot.Memberships, v)
	}
	for _, v := range s.agents {
		snapshot.Agents = append(snapshot.Agents, v)
	}
	for _, v := range s.orgTrusts {
		snapshot.OrgTrusts = append(snapshot.OrgTrusts, v)
	}
	for _, v := range s.agentTrusts {
		snapshot.AgentTrusts = append(snapshot.AgentTrusts, v)
	}
	for _, v := range s.statsByOrg {
		snapshot.Stats = append(snapshot.Stats, v)
	}
	snapshot.MessageMetrics = s.buildAdminMessageMetricsLocked()
	for _, events := range s.auditByOrg {
		snapshot.ActivityFeed = append(snapshot.ActivityFeed, events...)
	}
	sort.Slice(snapshot.ActivityFeed, func(i, j int) bool {
		if snapshot.ActivityFeed[i].CreatedAt.Equal(snapshot.ActivityFeed[j].CreatedAt) {
			return snapshot.ActivityFeed[i].EventID < snapshot.ActivityFeed[j].EventID
		}
		return snapshot.ActivityFeed[i].CreatedAt.Before(snapshot.ActivityFeed[j].CreatedAt)
	})

	return snapshot
}

func (s *MemoryStore) buildAdminMessageMetricsLocked() model.AdminMessageMetrics {
	agentMetricsByID := make(map[string]*model.AgentMessageMetrics, len(s.agents))
	for agentUUID, agent := range s.agents {
		metric := &model.AgentMessageMetrics{
			AgentUUID:    agentUUID,
			AgentID:      agent.AgentID,
			OrgID:        agent.OrgID,
			OwnerHumanID: agent.OwnerHumanID,
			AgentType:    agentTypeFromMetadata(agent.Metadata),
			Archive: model.AgentMessageArchive{
				From: make([]model.MessageArchiveEntry, 0),
				To:   make([]model.MessageArchiveEntry, 0),
			},
		}
		agentMetricsByID[agentUUID] = metric
	}

	records := make([]model.MessageRecord, 0, len(s.messageRecords))
	for _, record := range s.messageRecords {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Message.CreatedAt.Equal(records[j].Message.CreatedAt) {
			return records[i].Message.MessageID < records[j].Message.MessageID
		}
		return records[i].Message.CreatedAt.Before(records[j].Message.CreatedAt)
	})

	for _, record := range records {
		if senderMetrics, ok := agentMetricsByID[record.Message.FromAgentUUID]; ok {
			if entry, ok := fromArchiveEntry(record); ok {
				senderMetrics.OutboxMessages++
				senderMetrics.Archive.From = append(senderMetrics.Archive.From, entry)
			}
		}
		if receiverMetrics, ok := agentMetricsByID[record.Message.ToAgentUUID]; ok {
			if entry, ok := toArchiveEntry(record); ok {
				receiverMetrics.InboxMessages++
				receiverMetrics.Archive.To = append(receiverMetrics.Archive.To, entry)
			}
		}
	}

	agentIDs := make([]string, 0, len(agentMetricsByID))
	for agentUUID := range agentMetricsByID {
		agentIDs = append(agentIDs, agentUUID)
	}
	sort.Strings(agentIDs)
	agents := make([]model.AgentMessageMetrics, 0, len(agentIDs))
	for _, agentUUID := range agentIDs {
		agents = append(agents, *agentMetricsByID[agentUUID])
	}

	humans := buildHumanMessageMetricsLocked(s.humans, agents)
	organizations := buildOrganizationMessageMetricsLocked(s.orgs, agents)
	return model.AdminMessageMetrics{
		Agents:        agents,
		Humans:        humans,
		Organizations: organizations,
	}
}

func fromArchiveEntry(record model.MessageRecord) (model.MessageArchiveEntry, bool) {
	if strings.TrimSpace(record.Message.MessageID) == "" {
		return model.MessageArchiveEntry{}, false
	}
	return model.MessageArchiveEntry{
		MessageID:             record.Message.MessageID,
		CounterpartyAgentUUID: strings.TrimSpace(record.Message.ToAgentUUID),
		CounterpartyAgentID:   strings.TrimSpace(record.Message.ToAgentID),
		CounterpartyAgentURI:  strings.TrimSpace(record.Message.ToAgentURI),
		CounterpartyOrgID:     strings.TrimSpace(record.Message.ReceiverOrgID),
		ContentType:           normalizedArchiveContentType(record.Message.ContentType),
		PublishedAt:           record.Message.CreatedAt,
		FirstReceivedAt:       record.FirstReceivedAt,
		Status:                strings.TrimSpace(record.Status),
	}, true
}

func toArchiveEntry(record model.MessageRecord) (model.MessageArchiveEntry, bool) {
	if strings.TrimSpace(record.Message.MessageID) == "" || record.FirstReceivedAt == nil {
		return model.MessageArchiveEntry{}, false
	}
	return model.MessageArchiveEntry{
		MessageID:             record.Message.MessageID,
		CounterpartyAgentUUID: strings.TrimSpace(record.Message.FromAgentUUID),
		CounterpartyAgentID:   strings.TrimSpace(record.Message.FromAgentID),
		CounterpartyAgentURI:  strings.TrimSpace(record.Message.FromAgentURI),
		CounterpartyOrgID:     strings.TrimSpace(record.Message.SenderOrgID),
		ContentType:           normalizedArchiveContentType(record.Message.ContentType),
		PublishedAt:           record.Message.CreatedAt,
		FirstReceivedAt:       record.FirstReceivedAt,
		Status:                strings.TrimSpace(record.Status),
	}, true
}

func normalizedArchiveContentType(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "unknown"
	}
	return contentType
}

func messageAuditDetails(message model.Message) map[string]any {
	details := map[string]any{
		"from_agent_uuid": strings.TrimSpace(message.FromAgentUUID),
		"to_agent_uuid":   strings.TrimSpace(message.ToAgentUUID),
		"from_agent_id":   strings.TrimSpace(message.FromAgentID),
		"to_agent_id":     strings.TrimSpace(message.ToAgentID),
		"from_agent_uri":  strings.TrimSpace(message.FromAgentURI),
		"to_agent_uri":    strings.TrimSpace(message.ToAgentURI),
		"sender_org_id":   strings.TrimSpace(message.SenderOrgID),
		"receiver_org_id": strings.TrimSpace(message.ReceiverOrgID),
		"receiver_peer_id": strings.TrimSpace(
			message.ReceiverPeerID,
		),
		"content_type": normalizedArchiveContentType(message.ContentType),
	}
	if message.ClientMsgID != nil {
		details["client_msg_id"] = strings.TrimSpace(*message.ClientMsgID)
	}
	return details
}

func buildHumanMessageMetricsLocked(humans map[string]model.Human, agents []model.AgentMessageMetrics) []model.HumanMessageMetrics {
	byHumanID := make(map[string]*model.HumanMessageMetrics, len(humans))
	typeTotals := make(map[string]map[string]*model.AgentTypeMessageRollup, len(humans))
	for humanID := range humans {
		byHumanID[humanID] = &model.HumanMessageMetrics{
			HumanID:         humanID,
			AgentTypeTotals: make([]model.AgentTypeMessageRollup, 0),
		}
		typeTotals[humanID] = make(map[string]*model.AgentTypeMessageRollup)
	}

	for _, agent := range agents {
		if agent.OwnerHumanID == nil {
			continue
		}
		humanID := strings.TrimSpace(*agent.OwnerHumanID)
		metric, ok := byHumanID[humanID]
		if !ok {
			continue
		}
		metric.LinkedAgents++
		metric.OutboxMessages += agent.OutboxMessages
		metric.InboxMessages += agent.InboxMessages
		accumulateAgentTypeTotals(typeTotals[humanID], agent.AgentType, agent.OutboxMessages, agent.InboxMessages)
	}

	humanIDs := make([]string, 0, len(byHumanID))
	for humanID := range byHumanID {
		humanIDs = append(humanIDs, humanID)
	}
	sort.Strings(humanIDs)

	out := make([]model.HumanMessageMetrics, 0, len(humanIDs))
	for _, humanID := range humanIDs {
		metric := *byHumanID[humanID]
		metric.AgentTypeTotals = flattenTypeRollups(typeTotals[humanID])
		out = append(out, metric)
	}
	return out
}

func buildOrganizationMessageMetricsLocked(orgs map[string]model.Organization, agents []model.AgentMessageMetrics) []model.OrganizationMessageMetrics {
	byOrgID := make(map[string]*model.OrganizationMessageMetrics, len(orgs))
	typeTotals := make(map[string]map[string]*model.AgentTypeMessageRollup, len(orgs))
	for orgID := range orgs {
		byOrgID[orgID] = &model.OrganizationMessageMetrics{
			OrgID:           orgID,
			AgentTypeTotals: make([]model.AgentTypeMessageRollup, 0),
		}
		typeTotals[orgID] = make(map[string]*model.AgentTypeMessageRollup)
	}

	for _, agent := range agents {
		orgID := strings.TrimSpace(agent.OrgID)
		if orgID == "" {
			continue
		}
		metric, ok := byOrgID[orgID]
		if !ok {
			continue
		}
		metric.LinkedAgents++
		metric.OutboxMessages += agent.OutboxMessages
		metric.InboxMessages += agent.InboxMessages
		accumulateAgentTypeTotals(typeTotals[orgID], agent.AgentType, agent.OutboxMessages, agent.InboxMessages)
	}

	orgIDs := make([]string, 0, len(byOrgID))
	for orgID := range byOrgID {
		orgIDs = append(orgIDs, orgID)
	}
	sort.Strings(orgIDs)

	out := make([]model.OrganizationMessageMetrics, 0, len(orgIDs))
	for _, orgID := range orgIDs {
		metric := *byOrgID[orgID]
		metric.AgentTypeTotals = flattenTypeRollups(typeTotals[orgID])
		out = append(out, metric)
	}
	return out
}

func accumulateAgentTypeTotals(
	totals map[string]*model.AgentTypeMessageRollup,
	agentType string,
	outboxMessages, inboxMessages int64,
) {
	normalizedType := normalizeAgentTypeOrUnknown(agentType)
	rollup := totals[normalizedType]
	if rollup == nil {
		rollup = &model.AgentTypeMessageRollup{AgentType: normalizedType}
		totals[normalizedType] = rollup
	}
	rollup.AgentCount++
	rollup.OutboxMessages += outboxMessages
	rollup.InboxMessages += inboxMessages
}

func flattenTypeRollups(source map[string]*model.AgentTypeMessageRollup) []model.AgentTypeMessageRollup {
	if len(source) == 0 {
		return []model.AgentTypeMessageRollup{}
	}
	keys := make([]string, 0, len(source))
	for agentType := range source {
		keys = append(keys, agentType)
	}
	sort.Strings(keys)
	out := make([]model.AgentTypeMessageRollup, 0, len(keys))
	for _, agentType := range keys {
		out = append(out, *source[agentType])
	}
	return out
}

func (s *MemoryStore) CanPublish(senderAgentUUID, receiverAgentUUID string) (string, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sender, ok := s.agents[senderAgentUUID]
	if !ok || sender.Status == model.StatusRevoked {
		return "", "", ErrAgentNotFound
	}
	receiver, ok := s.agents[receiverAgentUUID]
	if !ok || receiver.Status == model.StatusRevoked {
		return "", "", ErrAgentNotFound
	}
	senderOrgID := sender.OrgID
	receiverOrgID := receiver.OrgID
	if !s.canPublishLocked(sender, receiver) {
		return senderOrgID, receiverOrgID, ErrNoTrustPath
	}

	return senderOrgID, receiverOrgID, nil
}

func (s *MemoryStore) canPublishLocked(sender, receiver model.Agent) bool {
	if sender.OrgID != receiver.OrgID {
		orgEdgeID, ok := s.orgTrustByPair[pairKey(sender.OrgID, receiver.OrgID)]
		if !ok {
			return false
		}
		orgEdge, ok := s.orgTrusts[orgEdgeID]
		if !ok || orgEdge.State != model.StatusActive {
			return false
		}
	}

	agentEdgeID, ok := s.agentTrustByPair[pairKey(sender.AgentUUID, receiver.AgentUUID)]
	if !ok {
		return false
	}
	agentEdge, ok := s.agentTrusts[agentEdgeID]
	if !ok || agentEdge.State != model.StatusActive {
		return false
	}
	return true
}

func (s *MemoryStore) CreateOrGetMessageRecord(message model.Message, acceptedAt time.Time) (model.MessageRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(message.MessageID) == "" {
		return model.MessageRecord{}, false, ErrMessageNotFound
	}
	if existing, ok := s.messageRecords[message.MessageID]; ok {
		existing.IdempotentReplays++
		existing.UpdatedAt = acceptedAt
		s.messageRecords[message.MessageID] = existing
		if orgID := strings.TrimSpace(existing.Message.SenderOrgID); orgID != "" {
			s.appendAuditLocked(orgID, "", "message", "publish_replay", existing.Message.MessageID, messageAuditDetails(existing.Message), acceptedAt)
		}
		return existing, true, nil
	}
	if message.ClientMsgID != nil {
		if existingID, ok := s.messageByClientMsg[messageClientKey(message.FromAgentUUID, *message.ClientMsgID)]; ok {
			record, ok := s.messageRecords[existingID]
			if !ok {
				delete(s.messageByClientMsg, messageClientKey(message.FromAgentUUID, *message.ClientMsgID))
			} else {
				record.IdempotentReplays++
				record.UpdatedAt = acceptedAt
				s.messageRecords[existingID] = record
				s.incrementDuplicateLocked(record.Message.SenderOrgID, acceptedAt)
				if orgID := strings.TrimSpace(record.Message.SenderOrgID); orgID != "" {
					s.appendAuditLocked(orgID, "", "message", "publish_replay", record.Message.MessageID, messageAuditDetails(record.Message), acceptedAt)
				}
				return record, true, nil
			}
		}
	}

	record := model.MessageRecord{
		Message:    message,
		Status:     model.MessageDeliveryQueued,
		AcceptedAt: acceptedAt,
		UpdatedAt:  acceptedAt,
	}
	s.messageRecords[message.MessageID] = record
	if message.ClientMsgID != nil {
		s.messageByClientMsg[messageClientKey(message.FromAgentUUID, *message.ClientMsgID)] = message.MessageID
	}
	if orgID := strings.TrimSpace(message.SenderOrgID); orgID != "" {
		s.appendAuditLocked(orgID, "", "message", "publish", message.MessageID, messageAuditDetails(message), acceptedAt)
	}
	return record, false, nil
}

func (s *MemoryStore) MarkMessageForwarded(messageID string, forwardedAt time.Time) (model.MessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.messageRecords[strings.TrimSpace(messageID)]
	if !ok {
		return model.MessageRecord{}, ErrMessageNotFound
	}
	record.Status = model.MessageForwarded
	record.UpdatedAt = forwardedAt
	record.LastFailureAt = nil
	record.LastFailureReason = ""
	s.messageRecords[record.Message.MessageID] = record
	if orgID := strings.TrimSpace(record.Message.SenderOrgID); orgID != "" {
		s.appendAuditLocked(orgID, "", "message", "forward", record.Message.MessageID, messageAuditDetails(record.Message), forwardedAt)
	}
	return record, nil
}

func (s *MemoryStore) AbortMessageRecord(messageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.messageRecords[messageID]
	if !ok {
		return ErrMessageNotFound
	}
	delete(s.messageRecords, messageID)
	if record.Message.ClientMsgID != nil {
		delete(s.messageByClientMsg, messageClientKey(record.Message.FromAgentUUID, *record.Message.ClientMsgID))
	}
	if orgID := strings.TrimSpace(record.Message.SenderOrgID); orgID != "" {
		s.appendAuditLocked(orgID, "", "message", "publish_abort", record.Message.MessageID, messageAuditDetails(record.Message), time.Now().UTC())
	}
	return nil
}

func (s *MemoryStore) GetMessageRecord(messageID string) (model.MessageRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	record, ok := s.messageRecords[messageID]
	if !ok {
		return model.MessageRecord{}, ErrMessageNotFound
	}
	return record, nil
}

func (s *MemoryStore) LeaseMessage(messageID, receiverAgentUUID, deliveryID string, leasedAt, leaseExpiresAt time.Time) (model.MessageDelivery, model.MessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.messageRecords[messageID]
	if !ok {
		return model.MessageDelivery{}, model.MessageRecord{}, ErrMessageNotFound
	}
	if record.Message.ToAgentUUID != receiverAgentUUID {
		return model.MessageDelivery{}, model.MessageRecord{}, ErrMessageDeliveryMismatch
	}
	record.Status = model.MessageDeliveryLeased
	record.UpdatedAt = leasedAt
	if record.FirstReceivedAt == nil {
		record.FirstReceivedAt = timePtr(leasedAt)
	}
	record.LastLeasedAt = timePtr(leasedAt)
	record.LeaseExpiresAt = timePtr(leaseExpiresAt)
	record.DeliveryAttempts++
	record.LastDeliveryID = stringPtr(deliveryID)
	if record.DeliveryAttempts > 1 {
		s.incrementRedeliveredLocked(record.Message.SenderOrgID, leasedAt)
	}
	delivery := model.MessageDelivery{
		DeliveryID:     deliveryID,
		MessageID:      record.Message.MessageID,
		AgentUUID:      receiverAgentUUID,
		Attempt:        record.DeliveryAttempts,
		LeasedAt:       leasedAt,
		LeaseExpiresAt: leaseExpiresAt,
	}
	s.messageRecords[messageID] = record
	s.messageDeliveries[deliveryID] = delivery
	if orgID := strings.TrimSpace(record.Message.ReceiverOrgID); orgID != "" {
		details := messageAuditDetails(record.Message)
		details["delivery_id"] = deliveryID
		details["attempt"] = record.DeliveryAttempts
		s.appendAuditLocked(orgID, "", "message", "lease", record.Message.MessageID, details, leasedAt)
	}
	return delivery, record, nil
}

func (s *MemoryStore) AckMessageDelivery(receiverAgentUUID, deliveryID string, ackedAt time.Time) (model.MessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delivery, ok := s.messageDeliveries[deliveryID]
	if !ok {
		return model.MessageRecord{}, ErrMessageDeliveryNotFound
	}
	if delivery.AgentUUID != receiverAgentUUID {
		return model.MessageRecord{}, ErrMessageDeliveryMismatch
	}
	record, ok := s.messageRecords[delivery.MessageID]
	if !ok {
		delete(s.messageDeliveries, deliveryID)
		return model.MessageRecord{}, ErrMessageNotFound
	}
	record.Status = model.MessageDeliveryAcked
	record.UpdatedAt = ackedAt
	record.AckedAt = timePtr(ackedAt)
	record.LeaseExpiresAt = nil
	record.LastFailureAt = nil
	record.LastFailureReason = ""
	delete(s.messageDeliveries, deliveryID)
	s.messageRecords[delivery.MessageID] = record
	s.incrementAckedLocked(record.Message.SenderOrgID, ackedAt)
	if orgID := strings.TrimSpace(record.Message.ReceiverOrgID); orgID != "" {
		details := messageAuditDetails(record.Message)
		details["delivery_id"] = deliveryID
		s.appendAuditLocked(orgID, "", "message", "ack", record.Message.MessageID, details, ackedAt)
	}
	return record, nil
}

func (s *MemoryStore) ReleaseMessageDelivery(receiverAgentUUID, deliveryID string, now time.Time, reason string) (model.Message, model.MessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delivery, ok := s.messageDeliveries[deliveryID]
	if !ok {
		return model.Message{}, model.MessageRecord{}, ErrMessageDeliveryNotFound
	}
	if delivery.AgentUUID != receiverAgentUUID {
		return model.Message{}, model.MessageRecord{}, ErrMessageDeliveryMismatch
	}
	record, ok := s.messageRecords[delivery.MessageID]
	if !ok {
		delete(s.messageDeliveries, deliveryID)
		return model.Message{}, model.MessageRecord{}, ErrMessageNotFound
	}
	delete(s.messageDeliveries, deliveryID)
	record.Status = model.MessageDeliveryQueued
	record.UpdatedAt = now
	record.LeaseExpiresAt = nil
	record.RequeueCount++
	record.LastFailureReason = strings.TrimSpace(reason)
	record.LastFailureAt = timePtr(now)
	s.messageRecords[delivery.MessageID] = record
	if orgID := strings.TrimSpace(record.Message.ReceiverOrgID); orgID != "" {
		details := messageAuditDetails(record.Message)
		details["delivery_id"] = deliveryID
		details["reason"] = record.LastFailureReason
		s.appendAuditLocked(orgID, "", "message", "nack", record.Message.MessageID, details, now)
	}
	return record.Message, record, nil
}

func (s *MemoryStore) ExpireMessageLeases(now time.Time) ([]model.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.messageDeliveries) == 0 {
		return nil, nil
	}

	deliveryIDs := make([]string, 0, len(s.messageDeliveries))
	for deliveryID := range s.messageDeliveries {
		deliveryIDs = append(deliveryIDs, deliveryID)
	}
	sort.Strings(deliveryIDs)

	var out []model.Message
	for _, deliveryID := range deliveryIDs {
		delivery := s.messageDeliveries[deliveryID]
		if delivery.LeaseExpiresAt.After(now) {
			continue
		}
		record, ok := s.messageRecords[delivery.MessageID]
		if !ok {
			delete(s.messageDeliveries, deliveryID)
			continue
		}
		delete(s.messageDeliveries, deliveryID)
		record.Status = model.MessageDeliveryQueued
		record.UpdatedAt = now
		record.LeaseExpiresAt = nil
		record.RequeueCount++
		record.LastFailureReason = "lease_expired"
		record.LastFailureAt = timePtr(now)
		s.messageRecords[delivery.MessageID] = record
		s.incrementExpiredLocked(record.Message.SenderOrgID, now)
		if orgID := strings.TrimSpace(record.Message.ReceiverOrgID); orgID != "" {
			details := messageAuditDetails(record.Message)
			details["delivery_id"] = deliveryID
			details["reason"] = "lease_expired"
			s.appendAuditLocked(orgID, "", "message", "lease_expire", record.Message.MessageID, details, now)
		}
		out = append(out, record.Message)
	}
	return out, nil
}

func (s *MemoryStore) GetQueueMetrics() model.QueueMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	metrics := model.QueueMetrics{}
	for _, queue := range s.queues {
		metrics.AvailableMessages += len(queue)
		for _, message := range queue {
			if metrics.OldestQueuedAt == nil || message.CreatedAt.Before(*metrics.OldestQueuedAt) {
				metrics.OldestQueuedAt = timePtr(message.CreatedAt)
			}
		}
	}
	metrics.LeasedMessages = len(s.messageDeliveries)
	for _, delivery := range s.messageDeliveries {
		if metrics.OldestLeaseExpiryAt == nil || delivery.LeaseExpiresAt.Before(*metrics.OldestLeaseExpiryAt) {
			metrics.OldestLeaseExpiryAt = timePtr(delivery.LeaseExpiresAt)
		}
	}
	return metrics
}

func (s *MemoryStore) EnqueuePeerOutbound(peerID, outboundID string, message model.Message, now time.Time) (model.PeerOutboundMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.peerInstances[peerID]; !ok {
		return model.PeerOutboundMessage{}, ErrPeerInstanceNotFound
	}
	outbound := model.PeerOutboundMessage{
		OutboundID:    outboundID,
		PeerID:        peerID,
		MessageID:     message.MessageID,
		Message:       message,
		Status:        model.StatusPending,
		CreatedAt:     now,
		UpdatedAt:     now,
		NextAttemptAt: now,
	}
	s.peerOutbounds[outbound.OutboundID] = outbound
	s.peerOutboundByPeer[peerID] = append(s.peerOutboundByPeer[peerID], outbound.OutboundID)
	return outbound, nil
}

func (s *MemoryStore) ListDuePeerOutbounds(now time.Time, limit int) []model.PeerOutboundMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 {
		limit = 32
	}
	out := make([]model.PeerOutboundMessage, 0, limit)
	for _, outbound := range s.peerOutbounds {
		if outbound.Status != model.StatusPending {
			continue
		}
		if outbound.NextAttemptAt.After(now) {
			continue
		}
		out = append(out, outbound)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeerID == out[j].PeerID {
			if out[i].NextAttemptAt.Equal(out[j].NextAttemptAt) {
				return out[i].OutboundID < out[j].OutboundID
			}
			return out[i].NextAttemptAt.Before(out[j].NextAttemptAt)
		}
		return out[i].PeerID < out[j].PeerID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (s *MemoryStore) MarkPeerOutboundRetry(outboundID, reason string, nextAttemptAt, now time.Time) (model.PeerOutboundMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	outbound, ok := s.peerOutbounds[strings.TrimSpace(outboundID)]
	if !ok {
		return model.PeerOutboundMessage{}, ErrMessageNotFound
	}
	outbound.AttemptCount++
	outbound.LastAttemptAt = timePtr(now)
	outbound.LastError = strings.TrimSpace(reason)
	outbound.NextAttemptAt = nextAttemptAt
	outbound.UpdatedAt = now
	s.peerOutbounds[outbound.OutboundID] = outbound
	return outbound, nil
}

func (s *MemoryStore) MarkPeerOutboundDelivered(outboundID string, now time.Time) (model.PeerOutboundMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	outbound, ok := s.peerOutbounds[strings.TrimSpace(outboundID)]
	if !ok {
		return model.PeerOutboundMessage{}, ErrMessageNotFound
	}
	outbound.AttemptCount++
	outbound.LastAttemptAt = timePtr(now)
	outbound.LastDeliveredAt = timePtr(now)
	outbound.Status = model.StatusActive
	outbound.UpdatedAt = now
	s.peerOutbounds[outbound.OutboundID] = outbound

	queue := s.peerOutboundByPeer[outbound.PeerID]
	filtered := queue[:0]
	for _, id := range queue {
		if id != outbound.OutboundID {
			filtered = append(filtered, id)
		}
	}
	if len(filtered) == 0 {
		delete(s.peerOutboundByPeer, outbound.PeerID)
	} else {
		s.peerOutboundByPeer[outbound.PeerID] = filtered
	}
	delete(s.peerOutbounds, outbound.OutboundID)
	return outbound, nil
}

func (s *MemoryStore) RecordMessageQueued(orgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.incrementQueuedLocked(orgID, time.Now().UTC())
}

func (s *MemoryStore) RecordMessageDropped(orgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.incrementDroppedLocked(orgID, now)
	if strings.TrimSpace(orgID) != "" {
		s.appendAuditLocked(orgID, "", "message", "drop", orgID, map[string]any{
			"reason": "no_trust_path_or_policy",
		}, now)
	}
}

func (s *MemoryStore) Enqueue(_ context.Context, message model.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if message.ToAgentUUID == "" {
		return ErrAgentNotFound
	}
	agent, ok := s.agents[message.ToAgentUUID]
	if !ok || agent.Status == model.StatusRevoked {
		return ErrAgentNotFound
	}
	message.ToAgentID = agent.AgentID
	s.queues[message.ToAgentUUID] = append(s.queues[message.ToAgentUUID], message)
	return nil
}

func (s *MemoryStore) Dequeue(_ context.Context, agentUUID string) (model.Message, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	queue := s.queues[agentUUID]
	if len(queue) == 0 {
		return model.Message{}, false, nil
	}
	msg := queue[0]
	s.queues[agentUUID] = queue[1:]
	return msg, true, nil
}

func (s *MemoryStore) createOrJoinTrustLocked(edgeType, leftInput, rightInput, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	if edgeType != "org" && edgeType != "agent" {
		return model.TrustEdge{}, false, ErrInvalidEdgeType
	}
	if leftInput == rightInput {
		return model.TrustEdge{}, false, ErrSelfTrust
	}

	leftID, rightID := canonicalPair(leftInput, rightInput)
	if edgeType == "org" {
		if _, ok := s.orgs[leftID]; !ok {
			return model.TrustEdge{}, false, ErrOrgNotFound
		}
		if _, ok := s.orgs[rightID]; !ok {
			return model.TrustEdge{}, false, ErrOrgNotFound
		}
		actorSideOrg := leftInput
		if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(actorSideOrg, actorHumanID), model.RoleAdmin) {
			return model.TrustEdge{}, false, ErrUnauthorizedRole
		}
		key := pairKey(leftID, rightID)
		if existingID, ok := s.orgTrustByPair[key]; ok {
			edge := s.orgTrusts[existingID]
			edge = applyTrustRequest(edge, actorSideOrg == edge.LeftID, now)
			s.orgTrusts[existingID] = edge
			s.appendAuditLocked(actorSideOrg, actorHumanID, "trust_org", "request", edge.EdgeID, map[string]any{
				"peer_org_id": opposite(edge, actorSideOrg),
				"state":       edge.State,
			}, now)
			return edge, false, nil
		}
		edge := model.TrustEdge{
			EdgeID:    edgeID,
			EdgeType:  "org",
			LeftID:    leftID,
			RightID:   rightID,
			State:     model.StatusPending,
			CreatedBy: actorHumanID,
			CreatedAt: now,
			UpdatedAt: now,
		}
		edge = applyTrustRequest(edge, actorSideOrg == edge.LeftID, now)
		s.orgTrusts[edge.EdgeID] = edge
		s.orgTrustByPair[key] = edge.EdgeID
		s.appendAuditLocked(actorSideOrg, actorHumanID, "trust_org", "request", edge.EdgeID, map[string]any{
			"peer_org_id": opposite(edge, actorSideOrg),
			"state":       edge.State,
		}, now)
		return edge, true, nil
	}

	leftAgent, ok := s.agents[leftID]
	if !ok {
		return model.TrustEdge{}, false, ErrAgentNotFound
	}
	if _, ok := s.agents[rightID]; !ok {
		return model.TrustEdge{}, false, ErrAgentNotFound
	}

	actorSideAgent, ok := s.agents[leftInput]
	if !ok {
		return model.TrustEdge{}, false, ErrAgentNotFound
	}
	actorSideOrg := actorSideAgent.OrgID
	actorCanApprove := isSuperAdmin || s.canManageAgentLocked(actorSideAgent, actorHumanID)
	if !isSuperAdmin && !s.canRequestAgentTrustLocked(actorSideAgent, actorHumanID) {
		return model.TrustEdge{}, false, ErrUnauthorizedRole
	}

	key := pairKey(leftID, rightID)
	if existingID, ok := s.agentTrustByPair[key]; ok {
		edge := s.agentTrusts[existingID]
		isLeftActor := leftInput == edge.LeftID
		edge = applyAgentTrustRequest(edge, isLeftActor, actorCanApprove, now)
		if isSuperAdmin || (s.canManageAgentLockedByID(edge.LeftID, actorHumanID) && s.canManageAgentLockedByID(edge.RightID, actorHumanID)) {
			edge.LeftApproved = true
			edge.RightApproved = true
			edge.State = model.StatusActive
			edge.UpdatedAt = now
		}
		s.agentTrusts[existingID] = edge
		s.appendAuditLocked(actorSideOrg, actorHumanID, "trust_agent", "request", edge.EdgeID, map[string]any{
			"peer_agent_uuid": opposite(edge, leftInput),
			"state":           edge.State,
		}, now)
		return edge, false, nil
	}

	edge := model.TrustEdge{
		EdgeID:    edgeID,
		EdgeType:  "agent",
		LeftID:    leftID,
		RightID:   rightID,
		State:     model.StatusPending,
		CreatedBy: actorHumanID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	edge = applyAgentTrustRequest(edge, leftInput == edge.LeftID, actorCanApprove, now)
	if isSuperAdmin || (s.canManageAgentLockedByID(edge.LeftID, actorHumanID) && s.canManageAgentLockedByID(edge.RightID, actorHumanID)) {
		edge.LeftApproved = true
		edge.RightApproved = true
		edge.State = model.StatusActive
		edge.UpdatedAt = now
	}
	s.agentTrusts[edge.EdgeID] = edge
	s.agentTrustByPair[key] = edge.EdgeID

	_ = leftAgent
	s.appendAuditLocked(actorSideOrg, actorHumanID, "trust_agent", "request", edge.EdgeID, map[string]any{
		"peer_agent_uuid": opposite(edge, leftInput),
		"state":           edge.State,
	}, now)
	return edge, true, nil
}

func (s *MemoryStore) approveTrustLocked(edgeType, edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	edge, actorLeft, orgID, err := s.loadEdgeForActorLocked(edgeType, edgeID, actorHumanID, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if edgeType == "agent" && (isSuperAdmin || (s.canManageAgentLockedByID(edge.LeftID, actorHumanID) && s.canManageAgentLockedByID(edge.RightID, actorHumanID))) {
		edge.LeftApproved = true
		edge.RightApproved = true
		edge.State = model.StatusActive
		edge.UpdatedAt = now
		s.storeEdgeLocked(edgeType, edge)
		s.appendAuditLocked(orgID, actorHumanID, "trust_"+edgeType, "approve", edge.EdgeID, map[string]any{"state": edge.State}, now)
		return edge, nil
	}
	if edge.State == model.StatusRevoked || edge.State == model.StatusBlocked {
		edge.State = model.StatusPending
	}
	if actorLeft {
		edge.LeftApproved = true
	} else {
		edge.RightApproved = true
	}
	if edge.LeftApproved && edge.RightApproved {
		edge.State = model.StatusActive
	} else {
		edge.State = model.StatusPending
	}
	edge.UpdatedAt = now
	s.storeEdgeLocked(edgeType, edge)
	s.appendAuditLocked(orgID, actorHumanID, "trust_"+edgeType, "approve", edge.EdgeID, map[string]any{"state": edge.State}, now)
	return edge, nil
}

func (s *MemoryStore) blockTrustLocked(edgeType, edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	edge, _, orgID, err := s.loadEdgeForActorLocked(edgeType, edgeID, actorHumanID, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	edge.State = model.StatusBlocked
	edge.LeftApproved = false
	edge.RightApproved = false
	edge.UpdatedAt = now
	s.storeEdgeLocked(edgeType, edge)
	s.appendAuditLocked(orgID, actorHumanID, "trust_"+edgeType, "block", edge.EdgeID, nil, now)
	return edge, nil
}

func (s *MemoryStore) revokeTrustLocked(edgeType, edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	edge, _, orgID, err := s.loadEdgeForActorLocked(edgeType, edgeID, actorHumanID, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	edge.State = model.StatusRevoked
	edge.LeftApproved = false
	edge.RightApproved = false
	edge.UpdatedAt = now
	s.storeEdgeLocked(edgeType, edge)
	s.appendAuditLocked(orgID, actorHumanID, "trust_"+edgeType, "revoke", edge.EdgeID, nil, now)
	return edge, nil
}

func (s *MemoryStore) loadEdgeForActorLocked(edgeType, edgeID, actorHumanID string, isSuperAdmin bool) (model.TrustEdge, bool, string, error) {
	var edge model.TrustEdge
	var ok bool
	if edgeType == "org" {
		edge, ok = s.orgTrusts[edgeID]
	} else if edgeType == "agent" {
		edge, ok = s.agentTrusts[edgeID]
	} else {
		return model.TrustEdge{}, false, "", ErrInvalidEdgeType
	}
	if !ok {
		return model.TrustEdge{}, false, "", ErrTrustNotFound
	}

	if edgeType == "org" {
		if isSuperAdmin {
			return edge, true, edge.LeftID, nil
		}
		leftRole := s.membershipRoleLocked(edge.LeftID, actorHumanID)
		rightRole := s.membershipRoleLocked(edge.RightID, actorHumanID)
		if hasRoleAtLeast(leftRole, model.RoleAdmin) {
			return edge, true, edge.LeftID, nil
		}
		if hasRoleAtLeast(rightRole, model.RoleAdmin) {
			return edge, false, edge.RightID, nil
		}
		return model.TrustEdge{}, false, "", ErrUnauthorizedRole
	}

	leftAgent, lok := s.agents[edge.LeftID]
	rightAgent, rok := s.agents[edge.RightID]
	if !lok || !rok {
		return model.TrustEdge{}, false, "", ErrAgentNotFound
	}
	if isSuperAdmin {
		return edge, true, leftAgent.OrgID, nil
	}
	if s.canManageAgentLocked(leftAgent, actorHumanID) {
		return edge, true, leftAgent.OrgID, nil
	}
	if s.canManageAgentLocked(rightAgent, actorHumanID) {
		return edge, false, rightAgent.OrgID, nil
	}
	return model.TrustEdge{}, false, "", ErrUnauthorizedRole
}

func (s *MemoryStore) storeEdgeLocked(edgeType string, edge model.TrustEdge) {
	if edgeType == "org" {
		s.orgTrusts[edge.EdgeID] = edge
		return
	}
	s.agentTrusts[edge.EdgeID] = edge
}

func (s *MemoryStore) membershipRoleLocked(orgID, humanID string) string {
	if membershipID, ok := s.membershipByOrgUser[orgHumanKey(orgID, humanID)]; ok {
		if membership, ok := s.memberships[membershipID]; ok && membership.Status == model.StatusActive {
			return membership.Role
		}
	}
	return ""
}

func (s *MemoryStore) canManageAgentLocked(agent model.Agent, humanID string) bool {
	if hasRoleAtLeast(s.membershipRoleLocked(agent.OrgID, humanID), model.RoleAdmin) {
		return true
	}
	return agent.OwnerHumanID != nil && *agent.OwnerHumanID == humanID
}

func (s *MemoryStore) canRequestAgentTrustLocked(agent model.Agent, humanID string) bool {
	if s.canManageAgentLocked(agent, humanID) {
		return true
	}
	if agent.OrgID == "" {
		return false
	}
	return s.membershipRoleLocked(agent.OrgID, humanID) != ""
}

func (s *MemoryStore) canDeleteAgentLocked(agent model.Agent, humanID string) bool {
	if agent.OrgID == "" {
		return agent.OwnerHumanID != nil && *agent.OwnerHumanID == humanID
	}
	return s.membershipRoleLocked(agent.OrgID, humanID) == model.RoleOwner
}

func (s *MemoryStore) canManageAgentLockedByID(agentID, humanID string) bool {
	agent, ok := s.agents[agentID]
	if !ok {
		return false
	}
	return s.canManageAgentLocked(agent, humanID)
}

func (s *MemoryStore) resolveAgentRefLocked(agentRef string) (string, error) {
	ref := handles.NormalizeAgentRef(agentRef)
	if ref == "" {
		return "", ErrAgentNotFound
	}

	if agent, ok := s.agents[ref]; ok && agent.Status != model.StatusRevoked {
		return ref, nil
	}
	if agentUUID, ok := s.agentByURI[ref]; ok {
		if agent, exists := s.agents[agentUUID]; exists && agent.Status != model.StatusRevoked {
			return agentUUID, nil
		}
	}
	if strings.Contains(ref, "/") {
		return "", ErrAgentNotFound
	}

	match := ""
	for id, agent := range s.agents {
		if agent.Status == model.StatusRevoked {
			continue
		}
		if agent.Handle != ref {
			continue
		}
		if match == "" {
			match = id
			continue
		}
		return "", ErrAgentAmbiguous
	}
	if match == "" {
		return "", ErrAgentNotFound
	}
	return match, nil
}

func (s *MemoryStore) appendAuditLocked(orgID, actorHumanID, category, action, subjectID string, details map[string]any, now time.Time) {
	events := s.auditByOrg[orgID]
	eventID := fmt.Sprintf("%d-%d", now.UnixNano(), len(events)+1)
	events = append(events, model.AuditEvent{
		EventID:    eventID,
		OrgID:      orgID,
		ActorHuman: actorHumanID,
		Category:   category,
		Action:     action,
		SubjectID:  subjectID,
		Details:    details,
		CreatedAt:  now,
	})
	if len(events) > 200 {
		events = events[len(events)-200:]
	}
	s.auditByOrg[orgID] = events
}

func (s *MemoryStore) ensureOrgStatsLocked(orgID string) model.OrgStats {
	stats, ok := s.statsByOrg[orgID]
	if !ok {
		stats = model.OrgStats{OrgID: orgID}
		s.statsByOrg[orgID] = stats
	}
	return stats
}

func (s *MemoryStore) incrementQueuedLocked(orgID string, now time.Time) {
	stats := s.ensureOrgStatsLocked(orgID)
	stats.QueuedMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 1, 0, 0, 0, 0, 0)
}

func (s *MemoryStore) incrementDroppedLocked(orgID string, now time.Time) {
	stats := s.ensureOrgStatsLocked(orgID)
	stats.DroppedMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 0, 1, 0, 0, 0, 0)
}

func (s *MemoryStore) incrementAckedLocked(orgID string, now time.Time) {
	stats := s.ensureOrgStatsLocked(orgID)
	stats.AckedMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 0, 0, 1, 0, 0, 0)
}

func (s *MemoryStore) incrementExpiredLocked(orgID string, now time.Time) {
	stats := s.ensureOrgStatsLocked(orgID)
	stats.ExpiredMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 0, 0, 0, 1, 0, 0)
}

func (s *MemoryStore) incrementRedeliveredLocked(orgID string, now time.Time) {
	stats := s.ensureOrgStatsLocked(orgID)
	stats.RedeliveredMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 0, 0, 0, 0, 1, 0)
}

func (s *MemoryStore) incrementDuplicateLocked(orgID string, now time.Time) {
	stats := s.ensureOrgStatsLocked(orgID)
	stats.DuplicateMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 0, 0, 0, 0, 0, 1)
}

func (s *MemoryStore) incrementDailyLocked(orgID string, now time.Time, queuedDelta, droppedDelta, ackedDelta, expiredDelta, redeliveredDelta, duplicateDelta int64) {
	dayKey := now.Format("2006-01-02")
	perOrg, ok := s.statsDaily[orgID]
	if !ok {
		perOrg = make(map[string]model.OrgDailyStats)
		s.statsDaily[orgID] = perOrg
	}
	day := perOrg[dayKey]
	day.Date = dayKey
	day.QueuedMessages += queuedDelta
	day.DroppedMessages += droppedDelta
	day.AckedMessages += ackedDelta
	day.ExpiredMessages += expiredDelta
	day.RedeliveredMessages += redeliveredDelta
	day.DuplicateMessages += duplicateDelta
	perOrg[dayKey] = day
}

func (s *MemoryStore) last7DaysLocked(orgID string, now time.Time) []model.OrgDailyStats {
	perOrg := s.statsDaily[orgID]
	out := make([]model.OrgDailyStats, 0, 7)
	start := now.AddDate(0, 0, -6)
	for i := 0; i < 7; i++ {
		day := start.AddDate(0, 0, i)
		dayKey := day.Format("2006-01-02")
		row, ok := perOrg[dayKey]
		if !ok {
			row = model.OrgDailyStats{
				Date:                dayKey,
				QueuedMessages:      0,
				DroppedMessages:     0,
				AckedMessages:       0,
				ExpiredMessages:     0,
				RedeliveredMessages: 0,
				DuplicateMessages:   0,
			}
		}
		out = append(out, row)
	}
	return out
}

func messageClientKey(senderAgentUUID, clientMsgID string) string {
	return strings.TrimSpace(senderAgentUUID) + "\x00" + strings.TrimSpace(clientMsgID)
}

func timePtr(value time.Time) *time.Time {
	out := value
	return &out
}

func stringPtr(value string) *string {
	out := value
	return &out
}

func deriveInviteStatus(invite model.Invite, now time.Time) model.Invite {
	if invite.Status == model.StatusPending && !invite.ExpiresAt.IsZero() && now.After(invite.ExpiresAt) {
		invite.Status = model.StatusExpired
		if invite.RevokedAt == nil {
			expiredAt := invite.ExpiresAt
			invite.RevokedAt = &expiredAt
		}
	}
	return invite
}

func applyTrustRequest(edge model.TrustEdge, isLeftRequester bool, now time.Time) model.TrustEdge {
	if edge.State == model.StatusBlocked || edge.State == model.StatusRevoked {
		edge.State = model.StatusPending
		edge.LeftApproved = false
		edge.RightApproved = false
	}
	if isLeftRequester {
		edge.LeftApproved = true
	} else {
		edge.RightApproved = true
	}
	if edge.LeftApproved && edge.RightApproved {
		edge.State = model.StatusActive
	} else {
		edge.State = model.StatusPending
	}
	edge.UpdatedAt = now
	return edge
}

func applyAgentTrustRequest(edge model.TrustEdge, isLeftRequester, requesterCanApprove bool, now time.Time) model.TrustEdge {
	if edge.State == model.StatusBlocked || edge.State == model.StatusRevoked {
		edge.State = model.StatusPending
		edge.LeftApproved = false
		edge.RightApproved = false
	}
	if requesterCanApprove {
		if isLeftRequester {
			edge.LeftApproved = true
		} else {
			edge.RightApproved = true
		}
	}
	if edge.LeftApproved && edge.RightApproved {
		edge.State = model.StatusActive
	} else {
		edge.State = model.StatusPending
	}
	edge.UpdatedAt = now
	return edge
}

func opposite(edge model.TrustEdge, id string) string {
	if edge.LeftID == id {
		return edge.RightID
	}
	return edge.LeftID
}

func normalizeOrgAccessScopes(scopes []string) []string {
	set := make(map[string]struct{})
	for _, raw := range scopes {
		scope := strings.ToLower(strings.TrimSpace(raw))
		switch scope {
		case model.OrgAccessScopeListHumans, model.OrgAccessScopeListAgents:
			set[scope] = struct{}{}
		default:
			continue
		}
	}
	out := make([]string, 0, len(set))
	for scope := range set {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

func newRandomUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		uint32(raw[0])<<24|uint32(raw[1])<<16|uint32(raw[2])<<8|uint32(raw[3]),
		uint16(raw[4])<<8|uint16(raw[5]),
		uint16(raw[6])<<8|uint16(raw[7]),
		uint16(raw[8])<<8|uint16(raw[9]),
		uint64(raw[10])<<40|uint64(raw[11])<<32|uint64(raw[12])<<24|uint64(raw[13])<<16|uint64(raw[14])<<8|uint64(raw[15]),
	), nil
}

func normalizeOrgHandleKey(handle string) string {
	return normalizeHumanHandleCandidate(handle)
}

func orgAccessScopeAllowed(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func hasRoleAtLeast(actualRole, minimumRole string) bool {
	return roleRank(actualRole) >= roleRank(minimumRole)
}

func isValidRole(role string) bool {
	switch role {
	case model.RoleOwner, model.RoleAdmin, model.RoleMember, model.RoleViewer:
		return true
	default:
		return false
	}
}

func roleRank(role string) int {
	switch role {
	case model.RoleOwner:
		return 4
	case model.RoleAdmin:
		return 3
	case model.RoleMember:
		return 2
	case model.RoleViewer:
		return 1
	default:
		return 0
	}
}

func authKey(provider, subject string) string {
	return provider + "\x00" + subject
}

func orgHumanKey(orgID, humanID string) string {
	return orgID + "\x00" + humanID
}

func orgOwnedAgentNameKey(orgID, agentID string) string {
	return orgID + "\x00" + normalizeAgentNameKey(agentID)
}

func humanOwnedAgentNameKey(orgID, humanID, agentID string) string {
	return orgID + "\x00" + humanID + "\x00" + normalizeAgentNameKey(agentID)
}

func normalizeAgentNameKey(agentID string) string {
	return handles.Normalize(agentID)
}

func normalizeHumanHandleCandidate(input string) string {
	return handles.Normalize(input)
}

func (s *MemoryStore) claimUniqueHumanHandleLocked(base string, humanID string) string {
	candidate := normalizeHumanHandleCandidate(base)
	if err := handles.ValidateHandle(candidate); err != nil {
		candidate = "human"
	}
	if existingID, exists := s.humanByHandle[candidate]; !exists || existingID == humanID {
		return candidate
	}
	for i := 2; ; i++ {
		suffix := fmt.Sprintf("-%d", i)
		prefixMax := 64 - len(suffix)
		if prefixMax < 1 {
			prefixMax = 1
		}
		prefix := candidate
		if len(prefix) > prefixMax {
			prefix = strings.Trim(prefix[:prefixMax], "._-")
			if prefix == "" {
				prefix = "human"
			}
		}
		next := prefix + suffix
		if err := handles.ValidateHandle(next); err != nil {
			continue
		}
		if existingID, exists := s.humanByHandle[next]; !exists || existingID == humanID {
			return next
		}
	}
}

func canonicalPair(a, b string) (string, string) {
	if a <= b {
		return a, b
	}
	return b, a
}

func pairKey(a, b string) string {
	left, right := canonicalPair(a, b)
	return left + "\x00" + right
}

func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	return strings.ToLower(raw)
}

func remoteOrgTrustKey(localOrgID, peerID, remoteOrgHandle string) string {
	return strings.TrimSpace(localOrgID) + "\x00" + strings.TrimSpace(peerID) + "\x00" + handles.Normalize(remoteOrgHandle)
}

func remoteAgentTrustKey(localAgentUUID, peerID, remoteAgentURI string) string {
	return strings.TrimSpace(localAgentUUID) + "\x00" + strings.TrimSpace(peerID) + "\x00" + strings.TrimSpace(remoteAgentURI)
}

func agentRefFromURI(agentURI string) string {
	agentURI = strings.TrimSpace(agentURI)
	parsed, err := url.Parse(agentURI)
	if err != nil {
		return ""
	}
	path := strings.Trim(strings.TrimSpace(parsed.Path), "/")
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "orgs/") || strings.HasPrefix(path, "humans/") {
		return ""
	}
	return path
}
