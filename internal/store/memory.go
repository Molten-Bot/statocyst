package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"statocyst/internal/handles"
	"statocyst/internal/model"
)

var (
	ErrInvalidToken         = errors.New("invalid token")
	ErrOrgNotFound          = errors.New("organization not found")
	ErrOrgNameTaken         = errors.New("organization name already exists")
	ErrOrgHandleTaken       = errors.New("organization handle already exists")
	ErrHumanNotFound        = errors.New("human not found")
	ErrHumanHandleTaken     = errors.New("human handle already exists")
	ErrInvalidHandle        = errors.New("invalid handle")
	ErrMembershipNotFound   = errors.New("membership not found")
	ErrInviteNotFound       = errors.New("invite not found")
	ErrInviteInvalid        = errors.New("invite invalid")
	ErrOrgAccessKeyNotFound = errors.New("org access key not found")
	ErrOrgAccessKeyInvalid  = errors.New("org access key invalid")
	ErrOrgAccessScopeDenied = errors.New("org access scope denied")
	ErrCannotRevokeOwner    = errors.New("cannot revoke owner membership")
	ErrAgentExists          = errors.New("agent already exists")
	ErrAgentNotFound        = errors.New("agent not found")
	ErrAgentAmbiguous       = errors.New("agent reference is ambiguous")
	ErrAgentRevoked         = errors.New("agent revoked")
	ErrTrustNotFound        = errors.New("trust edge not found")
	ErrUnauthorizedRole     = errors.New("unauthorized role")
	ErrInvalidRole          = errors.New("invalid role")
	ErrInvalidEdgeType      = errors.New("invalid edge type")
	ErrSelfTrust            = errors.New("self trust not allowed")
	ErrNoTrustPath          = errors.New("no trust path")
	ErrBindNotFound         = errors.New("bind token not found")
	ErrBindExpired          = errors.New("bind token expired")
	ErrBindUsed             = errors.New("bind token already used")
	ErrAgentLimitExceeded   = errors.New("agent limit exceeded")
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

	binds      map[string]model.BindToken
	bindByHash map[string]string

	orgTrusts      map[string]model.TrustEdge
	orgTrustByPair map[string]string

	agentTrusts      map[string]model.TrustEdge
	agentTrustByPair map[string]string

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
		binds:                  make(map[string]model.BindToken),
		bindByHash:             make(map[string]string),
		orgTrusts:              make(map[string]model.TrustEdge),
		orgTrustByPair:         make(map[string]string),
		agentTrusts:            make(map[string]model.TrustEdge),
		agentTrustByPair:       make(map[string]string),
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
		IsPublic:      true,
		CreatedAt:     now,
	}
	s.humans[humanID] = h
	s.humanByAuthKey[key] = humanID
	s.humanByHandle[h.Handle] = h.HumanID
	return h, nil
}

func (s *MemoryStore) UpdateHumanProfile(humanID, handle string, isPublic *bool, confirmHandle bool, now time.Time) (model.Human, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.humans[humanID]
	if !ok {
		return model.Human{}, ErrHumanNotFound
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
	if isPublic != nil {
		h.IsPublic = *isPublic
	}
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
		IsPublic:    true,
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
		IsPublic:    true,
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

	invite := model.Invite{
		InviteID:     inviteID,
		OrgID:        orgID,
		Email:        strings.ToLower(strings.TrimSpace(email)),
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
	s.appendAuditLocked(invite.OrgID, humanID, "invite", "accept", inviteID, nil, now)
	return mem, nil
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
			IsPublic:     h.IsPublic,
		})
	}
	return out, nil
}

func (s *MemoryStore) RegisterAgent(orgID, agentID string, ownerHumanID *string, tokenHash, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	org, ok := s.orgs[orgID]
	if !ok {
		return model.Agent{}, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleMember) {
		return model.Agent{}, ErrUnauthorizedRole
	}
	agentHandle := normalizeAgentNameKey(agentID)
	if err := handles.ValidateHandle(agentHandle); err != nil {
		return model.Agent{}, ErrInvalidHandle
	}

	var ownerHandle *string
	if ownerHumanID != nil {
		if s.membershipRoleLocked(orgID, *ownerHumanID) == "" {
			return model.Agent{}, ErrMembershipNotFound
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
		key := orgOwnedAgentNameKey(orgID, agentHandle)
		if _, exists := s.orgOwnedAgentNameIdx[key]; exists {
			return model.Agent{}, ErrAgentExists
		}
	}
	agentURI := handles.BuildAgentURI(org.Handle, ownerHandle, agentHandle)
	if _, exists := s.agentByURI[agentURI]; exists {
		return model.Agent{}, ErrAgentExists
	}
	agentUUID, err := newRandomUUID()
	if err != nil {
		return model.Agent{}, err
	}

	agent := model.Agent{
		AgentUUID:    agentUUID,
		AgentID:      agentURI,
		Handle:       agentHandle,
		OrgID:        orgID,
		OwnerHumanID: ownerHumanID,
		TokenHash:    tokenHash,
		Status:       model.StatusActive,
		IsPublic:     true,
		CreatedBy:    actorHumanID,
		CreatedAt:    now,
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
	s.appendAuditLocked(orgID, actorHumanID, "agent", "register", agent.AgentUUID, map[string]any{
		"agent_id":       agent.AgentID,
		"agent_uuid":     agent.AgentUUID,
		"owner_human_id": ownerHumanID,
		"handle":         agentHandle,
	}, now)
	return agent, nil
}

func (s *MemoryStore) CreateBindToken(orgID string, ownerHumanID *string, actorHumanID, bindID, bindTokenHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.BindToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.orgs[orgID]; !ok {
		return model.BindToken{}, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleMember) {
		return model.BindToken{}, ErrUnauthorizedRole
	}
	if ownerHumanID != nil && s.membershipRoleLocked(orgID, *ownerHumanID) == "" {
		return model.BindToken{}, ErrMembershipNotFound
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
	s.appendAuditLocked(orgID, actorHumanID, "agent_bind", "create", bind.BindID, map[string]any{
		"owner_human_id": ownerHumanID,
		"expires_at":     expiresAt,
	}, now)
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
	org, ok := s.orgs[bind.OrgID]
	if !ok {
		return model.Agent{}, ErrOrgNotFound
	}
	var ownerHandle *string
	if bind.OwnerHumanID != nil {
		if s.membershipRoleLocked(bind.OrgID, *bind.OwnerHumanID) == "" {
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
		if _, exists := s.orgOwnedAgentNameIdx[orgOwnedAgentNameKey(bind.OrgID, agentHandle)]; exists {
			return model.Agent{}, ErrAgentExists
		}
	}
	agentURI := handles.BuildAgentURI(org.Handle, ownerHandle, agentHandle)
	if _, exists := s.agentByURI[agentURI]; exists {
		return model.Agent{}, ErrAgentExists
	}
	agentUUID, err := newRandomUUID()
	if err != nil {
		return model.Agent{}, err
	}

	agent := model.Agent{
		AgentUUID:    agentUUID,
		AgentID:      agentURI,
		Handle:       agentHandle,
		OrgID:        bind.OrgID,
		OwnerHumanID: bind.OwnerHumanID,
		TokenHash:    agentTokenHash,
		Status:       model.StatusActive,
		IsPublic:     true,
		CreatedBy:    bind.CreatedBy,
		CreatedAt:    now,
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
	s.appendAuditLocked(bind.OrgID, bind.CreatedBy, "agent_bind", "redeem", bind.BindID, map[string]any{
		"agent_id":   agent.AgentID,
		"agent_uuid": agent.AgentUUID,
	}, now)
	return agent, nil
}

func (s *MemoryStore) RotateAgentToken(agentUUID, actorHumanID, tokenHash string, now time.Time, isSuperAdmin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok {
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
	s.appendAuditLocked(agent.OrgID, actorHumanID, "agent", "revoke", agentUUID, map[string]any{
		"agent_id": agent.AgentID,
	}, now)
	return nil
}

func (s *MemoryStore) SetOrgVisibility(orgID string, isPublic bool, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	org, ok := s.orgs[orgID]
	if !ok {
		return model.Organization{}, ErrOrgNotFound
	}
	if !isSuperAdmin && !hasRoleAtLeast(s.membershipRoleLocked(orgID, actorHumanID), model.RoleAdmin) {
		return model.Organization{}, ErrUnauthorizedRole
	}
	org.IsPublic = isPublic
	s.orgs[orgID] = org
	s.appendAuditLocked(orgID, actorHumanID, "org", "set_visibility", orgID, map[string]any{"is_public": isPublic}, now)
	return org, nil
}

func (s *MemoryStore) SetAgentVisibility(agentUUID string, isPublic bool, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	agent, ok := s.agents[agentUUID]
	if !ok {
		return model.Agent{}, ErrAgentNotFound
	}
	if !isSuperAdmin && !s.canManageAgentLocked(agent, actorHumanID) {
		return model.Agent{}, ErrUnauthorizedRole
	}
	agent.IsPublic = isPublic
	s.agents[agentUUID] = agent
	s.appendAuditLocked(agent.OrgID, actorHumanID, "agent", "set_visibility", agentUUID, map[string]any{
		"is_public": isPublic,
		"agent_id":  agent.AgentID,
	}, now)
	return agent, nil
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

func (s *MemoryStore) CreateOrJoinOrgTrust(orgID, peerOrgID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createOrJoinTrustLocked("org", orgID, peerOrgID, actorHumanID, edgeID, now, isSuperAdmin)
}

func (s *MemoryStore) CreateOrJoinAgentTrust(orgID, agentUUID, peerAgentUUID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	a, ok := s.agents[agentUUID]
	if !ok {
		return model.TrustEdge{}, false, ErrAgentNotFound
	}
	if orgID != "" && a.OrgID != orgID {
		return model.TrustEdge{}, false, ErrUnauthorizedRole
	}
	if !isSuperAdmin && !s.canManageAgentLocked(a, actorHumanID) {
		return model.TrustEdge{}, false, ErrUnauthorizedRole
	}
	if _, ok := s.agents[peerAgentUUID]; !ok {
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

	return snapshot
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

func (s *MemoryStore) RecordMessageQueued(orgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	stats := s.ensureOrgStatsLocked(orgID)
	stats.QueuedMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 1, 0)
}

func (s *MemoryStore) RecordMessageDropped(orgID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	stats := s.ensureOrgStatsLocked(orgID)
	stats.DroppedMessages++
	s.statsByOrg[orgID] = stats
	s.incrementDailyLocked(orgID, now, 0, 1)
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
	if !isSuperAdmin && !s.canManageAgentLocked(actorSideAgent, actorHumanID) {
		return model.TrustEdge{}, false, ErrUnauthorizedRole
	}

	key := pairKey(leftID, rightID)
	if existingID, ok := s.agentTrustByPair[key]; ok {
		edge := s.agentTrusts[existingID]
		isLeftActor := leftInput == edge.LeftID
		edge = applyTrustRequest(edge, isLeftActor, now)
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
	edge = applyTrustRequest(edge, leftInput == edge.LeftID, now)
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
	events = append(events, model.AuditEvent{
		EventID:    fmt.Sprintf("%d", now.UnixNano()),
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

func (s *MemoryStore) incrementDailyLocked(orgID string, now time.Time, queuedDelta, droppedDelta int64) {
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
				Date:            dayKey,
				QueuedMessages:  0,
				DroppedMessages: 0,
			}
		}
		out = append(out, row)
	}
	return out
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
