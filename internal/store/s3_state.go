package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"statocyst/internal/model"
)

const (
	defaultS3StateRegion = "us-east-1"
	defaultS3StatePrefix = "statocyst-state"
)

type s3StateStore struct {
	*MemoryStore

	httpClient *http.Client
	endpoint   string
	bucket     string
	region     string
	prefix     string
	pathStyle  bool
	signer     *s3Signer

	persistMu sync.Mutex
	// persistedObjects tracks the last successfully persisted object bodies by key.
	// It lets us write only changed keys instead of full-prefix rewrites.
	persistedObjects map[string][]byte
}

type s3StateListBucketResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

type s3IndexValue struct {
	Value string `json:"value"`
}

type s3PersonalOrgValue struct {
	OrgID string `json:"org_id"`
}

type s3PersistInvite struct {
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
	InviteSecret string     `json:"invite_secret"`
}

type s3PersistAgent struct {
	AgentUUID    string     `json:"agent_uuid"`
	AgentID      string     `json:"agent_id"`
	Handle       string     `json:"handle"`
	OrgID        string     `json:"org_id"`
	OwnerHumanID *string    `json:"owner_human_id,omitempty"`
	TokenHash    string     `json:"token_hash"`
	Status       string     `json:"status"`
	IsPublic     bool       `json:"is_public"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
}

type s3PersistBindToken struct {
	BindID       string     `json:"bind_id"`
	OrgID        string     `json:"org_id"`
	OwnerHumanID *string    `json:"owner_human_id,omitempty"`
	TokenHash    string     `json:"token_hash"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	UsedAt       *time.Time `json:"used_at,omitempty"`
}

type s3PersistOrgAccessKey struct {
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
	TokenHash  string     `json:"token_hash"`
}

type s3KeyDescriptor struct {
	Prefix string
}

var _ ControlPlaneStore = (*s3StateStore)(nil)
var _ MessageQueueStore = (*s3StateStore)(nil)

func NewS3StateStoreFromEnv() (*s3StateStore, error) {
	endpoint := strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_ENDPOINT"))
	bucket := strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_BUCKET"))
	region := strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_REGION"))
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_PREFIX")), "/")
	pathStyleRaw := strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_PATH_STYLE"))
	accessKeyID := strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(os.Getenv("STATOCYST_STATE_S3_SECRET_ACCESS_KEY"))

	if endpoint == "" {
		return nil, fmt.Errorf("STATOCYST_STATE_S3_ENDPOINT is required for s3 state backend")
	}
	if bucket == "" {
		return nil, fmt.Errorf("STATOCYST_STATE_S3_BUCKET is required for s3 state backend")
	}
	if region == "" {
		region = defaultS3StateRegion
	}
	if prefix == "" {
		prefix = defaultS3StatePrefix
	}
	if (accessKeyID == "") != (secretAccessKey == "") {
		return nil, fmt.Errorf("STATOCYST_STATE_S3_ACCESS_KEY_ID and STATOCYST_STATE_S3_SECRET_ACCESS_KEY must be set together")
	}
	pathStyle := true
	if pathStyleRaw != "" {
		pathStyle = strings.EqualFold(pathStyleRaw, "true")
	}
	if !pathStyle {
		return nil, fmt.Errorf("STATOCYST_STATE_S3_PATH_STYLE=false is not supported in this build")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("STATOCYST_STATE_S3_ENDPOINT must include http:// or https:// scheme")
	}

	store := &s3StateStore{
		MemoryStore: NewMemoryStore(),
		httpClient:  &http.Client{Timeout: 10 * time.Second},
		endpoint:    strings.TrimSuffix(endpoint, "/"),
		bucket:      bucket,
		region:      region,
		prefix:      prefix,
		pathStyle:   pathStyle,
		signer:      newS3Signer(accessKeyID, secretAccessKey, region),
	}
	if err := store.loadFromS3(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *s3StateStore) UpsertHuman(provider, subject, email string, emailVerified bool, now time.Time, idFactory func() (string, error)) (model.Human, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	var existing model.Human
	existingFound := false
	authLookupKey := authKey(provider, subject)
	s.MemoryStore.mu.RLock()
	if humanID, ok := s.MemoryStore.humanByAuthKey[authLookupKey]; ok {
		if human, ok := s.MemoryStore.humans[humanID]; ok {
			existing = human
			existingFound = true
		}
	}
	s.MemoryStore.mu.RUnlock()

	human, err := s.MemoryStore.UpsertHuman(provider, subject, email, emailVerified, now, idFactory)
	if err != nil {
		return model.Human{}, err
	}
	if existingFound && existing == human {
		return human, nil
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Human{}, err
	}
	return human, nil
}

func (s *s3StateStore) UpdateHumanProfile(humanID, handle string, isPublic *bool, confirmHandle bool, now time.Time) (model.Human, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	human, err := s.MemoryStore.UpdateHumanProfile(humanID, handle, isPublic, confirmHandle, now)
	if err != nil {
		return model.Human{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Human{}, err
	}
	return human, nil
}

func (s *s3StateStore) CreateOrg(handle, displayName string, creatorHumanID string, orgID string, now time.Time) (model.Organization, model.Membership, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	org, membership, err := s.MemoryStore.CreateOrg(handle, displayName, creatorHumanID, orgID, now)
	if err != nil {
		return model.Organization{}, model.Membership{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Organization{}, model.Membership{}, err
	}
	return org, membership, nil
}

func (s *s3StateStore) EnsurePersonalOrg(humanID string, now time.Time, idFactory func() (string, error)) (model.Organization, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	org, err := s.MemoryStore.EnsurePersonalOrg(humanID, now, idFactory)
	if err != nil {
		return model.Organization{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Organization{}, err
	}
	return org, nil
}

func (s *s3StateStore) CreateInvite(orgID, email, role, actorHumanID, inviteID, inviteSecretHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.Invite, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	invite, err := s.MemoryStore.CreateInvite(orgID, email, role, actorHumanID, inviteID, inviteSecretHash, expiresAt, now, isSuperAdmin)
	if err != nil {
		return model.Invite{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Invite{}, err
	}
	return invite, nil
}

func (s *s3StateStore) AcceptInvite(inviteID, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	membership, err := s.MemoryStore.AcceptInvite(inviteID, humanID, humanEmail, now, idFactory)
	if err != nil {
		return model.Membership{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Membership{}, err
	}
	return membership, nil
}

func (s *s3StateStore) AcceptInviteBySecretHash(inviteSecretHash, humanID, humanEmail string, now time.Time, idFactory func() (string, error)) (model.Membership, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	membership, err := s.MemoryStore.AcceptInviteBySecretHash(inviteSecretHash, humanID, humanEmail, now, idFactory)
	if err != nil {
		return model.Membership{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Membership{}, err
	}
	return membership, nil
}

func (s *s3StateStore) RevokeInvite(inviteID, actorHumanID, actorEmail string, isSuperAdmin bool, now time.Time) (model.Invite, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	invite, err := s.MemoryStore.RevokeInvite(inviteID, actorHumanID, actorEmail, isSuperAdmin, now)
	if err != nil {
		return model.Invite{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Invite{}, err
	}
	return invite, nil
}

func (s *s3StateStore) RevokeMembership(orgID, humanID, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Membership, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	membership, err := s.MemoryStore.RevokeMembership(orgID, humanID, actorHumanID, isSuperAdmin, now)
	if err != nil {
		return model.Membership{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Membership{}, err
	}
	return membership, nil
}

func (s *s3StateStore) CreateOrgAccessKey(
	orgID, label string,
	scopes []string,
	expiresAt *time.Time,
	actorHumanID, keyID, tokenHash string,
	now time.Time,
	isSuperAdmin bool,
) (model.OrgAccessKey, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	key, err := s.MemoryStore.CreateOrgAccessKey(orgID, label, scopes, expiresAt, actorHumanID, keyID, tokenHash, now, isSuperAdmin)
	if err != nil {
		return model.OrgAccessKey{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.OrgAccessKey{}, err
	}
	return key, nil
}

func (s *s3StateStore) RevokeOrgAccessKey(orgID, keyID, actorHumanID string, isSuperAdmin bool, now time.Time) (model.OrgAccessKey, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	key, err := s.MemoryStore.RevokeOrgAccessKey(orgID, keyID, actorHumanID, isSuperAdmin, now)
	if err != nil {
		return model.OrgAccessKey{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.OrgAccessKey{}, err
	}
	return key, nil
}

func (s *s3StateStore) AuthorizeOrgAccessByName(orgName, accessKeyHash, requiredScope string, now time.Time) (model.Organization, model.OrgAccessKey, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	org, key, err := s.MemoryStore.AuthorizeOrgAccessByName(orgName, accessKeyHash, requiredScope, now)
	if err != nil {
		return model.Organization{}, model.OrgAccessKey{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Organization{}, model.OrgAccessKey{}, err
	}
	return org, key, nil
}

func (s *s3StateStore) RegisterAgent(orgID, agentID string, ownerHumanID *string, tokenHash, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.RegisterAgent(orgID, agentID, ownerHumanID, tokenHash, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) CreateBindToken(orgID string, ownerHumanID *string, actorHumanID, bindID, bindTokenHash string, expiresAt, now time.Time, isSuperAdmin bool) (model.BindToken, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	bind, err := s.MemoryStore.CreateBindToken(orgID, ownerHumanID, actorHumanID, bindID, bindTokenHash, expiresAt, now, isSuperAdmin)
	if err != nil {
		return model.BindToken{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.BindToken{}, err
	}
	return bind, nil
}

func (s *s3StateStore) RedeemBindToken(bindTokenHash, agentID, agentTokenHash string, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.RedeemBindToken(bindTokenHash, agentID, agentTokenHash, now)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) RotateAgentToken(agentUUID, actorHumanID, tokenHash string, now time.Time, isSuperAdmin bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if err := s.MemoryStore.RotateAgentToken(agentUUID, actorHumanID, tokenHash, now, isSuperAdmin); err != nil {
		return err
	}
	return s.persistAll(context.Background())
}

func (s *s3StateStore) RevokeAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if err := s.MemoryStore.RevokeAgent(agentUUID, actorHumanID, now, isSuperAdmin); err != nil {
		return err
	}
	return s.persistAll(context.Background())
}

func (s *s3StateStore) SetOrgVisibility(orgID string, isPublic bool, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Organization, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	org, err := s.MemoryStore.SetOrgVisibility(orgID, isPublic, actorHumanID, isSuperAdmin, now)
	if err != nil {
		return model.Organization{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Organization{}, err
	}
	return org, nil
}

func (s *s3StateStore) SetAgentVisibility(agentUUID string, isPublic bool, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.SetAgentVisibility(agentUUID, isPublic, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) SetAgentVisibilitySelf(agentUUID string, isPublic bool, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.SetAgentVisibilitySelf(agentUUID, isPublic, now)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) CreateOrJoinOrgTrust(orgID, peerOrgID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, created, err := s.MemoryStore.CreateOrJoinOrgTrust(orgID, peerOrgID, actorHumanID, edgeID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, false, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, false, err
	}
	return edge, created, nil
}

func (s *s3StateStore) CreateOrJoinAgentTrust(orgID, agentUUID, peerAgentUUID, actorHumanID, edgeID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, bool, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, created, err := s.MemoryStore.CreateOrJoinAgentTrust(orgID, agentUUID, peerAgentUUID, actorHumanID, edgeID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, false, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, false, err
	}
	return edge, created, nil
}

func (s *s3StateStore) ApproveOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, err := s.MemoryStore.ApproveOrgTrust(edgeID, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, err
	}
	return edge, nil
}

func (s *s3StateStore) BlockOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, err := s.MemoryStore.BlockOrgTrust(edgeID, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, err
	}
	return edge, nil
}

func (s *s3StateStore) RevokeOrgTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, err := s.MemoryStore.RevokeOrgTrust(edgeID, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, err
	}
	return edge, nil
}

func (s *s3StateStore) ApproveAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, err := s.MemoryStore.ApproveAgentTrust(edgeID, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, err
	}
	return edge, nil
}

func (s *s3StateStore) BlockAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, err := s.MemoryStore.BlockAgentTrust(edgeID, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, err
	}
	return edge, nil
}

func (s *s3StateStore) RevokeAgentTrust(edgeID, actorHumanID string, now time.Time, isSuperAdmin bool) (model.TrustEdge, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	edge, err := s.MemoryStore.RevokeAgentTrust(edgeID, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.TrustEdge{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.TrustEdge{}, err
	}
	return edge, nil
}

func (s *s3StateStore) RecordMessageQueued(orgID string) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.MemoryStore.RecordMessageQueued(orgID)
	_ = s.persistAll(context.Background())
}

func (s *s3StateStore) RecordMessageDropped(orgID string) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.MemoryStore.RecordMessageDropped(orgID)
	_ = s.persistAll(context.Background())
}

func (s *s3StateStore) persistAll(ctx context.Context) error {
	desired := flattenS3Objects(s.buildDesiredObjects())
	previous := s.persistedObjects
	if previous == nil {
		previous = make(map[string][]byte)
	}

	writeKeys := make([]string, 0, len(desired))
	for key, body := range desired {
		if existing, ok := previous[key]; ok && bytes.Equal(existing, body) {
			continue
		}
		writeKeys = append(writeKeys, key)
	}
	sort.Strings(writeKeys)
	for _, key := range writeKeys {
		if err := s.putObject(ctx, key, desired[key]); err != nil {
			return err
		}
	}

	deleteKeys := make([]string, 0)
	for key := range previous {
		if _, ok := desired[key]; ok {
			continue
		}
		deleteKeys = append(deleteKeys, key)
	}
	sort.Strings(deleteKeys)
	for _, key := range deleteKeys {
		if err := s.deleteObject(ctx, key); err != nil {
			return err
		}
	}

	s.persistedObjects = cloneS3Objects(desired)
	return nil
}

func (s *s3StateStore) buildDesiredObjects() map[string]map[string][]byte {
	s.MemoryStore.mu.RLock()
	defer s.MemoryStore.mu.RUnlock()

	desired := make(map[string]map[string][]byte)
	for _, descriptor := range s.persistedPrefixes() {
		desired[descriptor.Prefix] = make(map[string][]byte)
	}

	add := func(prefix, key string, value any) {
		body, err := json.Marshal(value)
		if err != nil {
			return
		}
		desired[prefix][key] = body
	}

	pHumans := s.prefixed("state/humans")
	pAgents := s.prefixed("state/agents")
	pOrgs := s.prefixed("state/orgs")
	pMemberships := s.prefixed("state/memberships")
	pInvites := s.prefixed("state/invites")
	pAccessKeys := s.prefixed("state/org_access_keys")
	pBinds := s.prefixed("state/binds")
	pOrgTrusts := s.prefixed("state/org_trusts")
	pAgentTrusts := s.prefixed("state/agent_trusts")
	pStats := s.prefixed("state/stats")
	pStatsDaily := s.prefixed("state/stats_daily")
	pAudit := s.prefixed("state/audit")
	pPersonalOrgs := s.prefixed("state/personal_orgs")

	for id, human := range s.MemoryStore.humans {
		add(pHumans, s.objectKey(pHumans, escapeKeySegment(id)+".json"), human)
	}
	for id, agent := range s.MemoryStore.agents {
		add(pAgents, s.objectKey(pAgents, escapeKeySegment(id)+".json"), persistAgent(agent))
	}
	for id, org := range s.MemoryStore.orgs {
		add(pOrgs, s.objectKey(pOrgs, escapeKeySegment(id)+".json"), org)
	}
	for id, membership := range s.MemoryStore.memberships {
		add(pMemberships, s.objectKey(pMemberships, escapeKeySegment(id)+".json"), membership)
	}
	for id, invite := range s.MemoryStore.invites {
		add(pInvites, s.objectKey(pInvites, escapeKeySegment(id)+".json"), persistInvite(invite))
	}
	for id, key := range s.MemoryStore.orgAccessKeys {
		add(pAccessKeys, s.objectKey(pAccessKeys, escapeKeySegment(id)+".json"), persistOrgAccessKey(key))
	}
	for id, bind := range s.MemoryStore.binds {
		add(pBinds, s.objectKey(pBinds, escapeKeySegment(id)+".json"), persistBindToken(bind))
	}
	for id, edge := range s.MemoryStore.orgTrusts {
		add(pOrgTrusts, s.objectKey(pOrgTrusts, escapeKeySegment(id)+".json"), edge)
	}
	for id, edge := range s.MemoryStore.agentTrusts {
		add(pAgentTrusts, s.objectKey(pAgentTrusts, escapeKeySegment(id)+".json"), edge)
	}
	for orgID, stats := range s.MemoryStore.statsByOrg {
		add(pStats, s.objectKey(pStats, escapeKeySegment(orgID)+".json"), stats)
	}
	for orgID, perDay := range s.MemoryStore.statsDaily {
		for day, row := range perDay {
			add(pStatsDaily, s.objectKey(pStatsDaily, escapeKeySegment(orgID), escapeKeySegment(day)+".json"), row)
		}
	}
	for orgID, events := range s.MemoryStore.auditByOrg {
		for _, event := range events {
			eventID := event.EventID
			if strings.TrimSpace(eventID) == "" {
				continue
			}
			add(pAudit, s.objectKey(pAudit, escapeKeySegment(orgID), escapeKeySegment(eventID)+".json"), event)
		}
	}
	for humanID, orgID := range s.MemoryStore.personalOrgByHuman {
		add(pPersonalOrgs, s.objectKey(pPersonalOrgs, escapeKeySegment(humanID)+".json"), s3PersonalOrgValue{OrgID: orgID})
	}

	pIdxHumanAuth := s.prefixed("idx/humans/by_auth")
	pIdxHumanHandle := s.prefixed("idx/humans/by_handle")
	pIdxOrgHandle := s.prefixed("idx/orgs/by_handle")
	pIdxMembership := s.prefixed("idx/memberships/by_org_human")
	pIdxInviteSecret := s.prefixed("idx/invites/by_secret_hash")
	pIdxAccessToken := s.prefixed("idx/org_access_keys/by_token_hash")
	pIdxAgentToken := s.prefixed("idx/agents/by_token_hash")
	pIdxAgentURI := s.prefixed("idx/agents/by_uri")
	pIdxOrgTrustPair := s.prefixed("idx/org_trusts/by_pair")
	pIdxAgentTrustPair := s.prefixed("idx/agent_trusts/by_pair")

	for key, humanID := range s.MemoryStore.humanByAuthKey {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		add(pIdxHumanAuth, s.objectKey(pIdxHumanAuth, escapeKeySegment(parts[0]), escapeKeySegment(parts[1])+".json"), s3IndexValue{Value: humanID})
	}
	for handle, humanID := range s.MemoryStore.humanByHandle {
		add(pIdxHumanHandle, s.objectKey(pIdxHumanHandle, escapeKeySegment(handle)+".json"), s3IndexValue{Value: humanID})
	}
	for handle, orgID := range s.MemoryStore.orgByHandle {
		add(pIdxOrgHandle, s.objectKey(pIdxOrgHandle, escapeKeySegment(handle)+".json"), s3IndexValue{Value: orgID})
	}
	for key, membershipID := range s.MemoryStore.membershipByOrgUser {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		add(pIdxMembership, s.objectKey(pIdxMembership, escapeKeySegment(parts[0]), escapeKeySegment(parts[1])+".json"), s3IndexValue{Value: membershipID})
	}
	for secretHash, inviteID := range s.MemoryStore.inviteBySecretHash {
		add(pIdxInviteSecret, s.objectKey(pIdxInviteSecret, escapeKeySegment(secretHash)+".json"), s3IndexValue{Value: inviteID})
	}
	for tokenHash, keyID := range s.MemoryStore.orgAccessKeyByHash {
		add(pIdxAccessToken, s.objectKey(pIdxAccessToken, escapeKeySegment(tokenHash)+".json"), s3IndexValue{Value: keyID})
	}
	for tokenHash, agentUUID := range s.MemoryStore.agentTokenIdx {
		add(pIdxAgentToken, s.objectKey(pIdxAgentToken, escapeKeySegment(tokenHash)+".json"), s3IndexValue{Value: agentUUID})
	}
	for agentURI, agentUUID := range s.MemoryStore.agentByURI {
		add(pIdxAgentURI, s.objectKey(pIdxAgentURI, hashKey(agentURI)+".json"), s3IndexValue{Value: agentUUID})
	}
	for key, edgeID := range s.MemoryStore.orgTrustByPair {
		left, right := decodePairIndexKey(key)
		add(pIdxOrgTrustPair, s.objectKey(pIdxOrgTrustPair, escapeKeySegment(left)+"_"+escapeKeySegment(right)+".json"), s3IndexValue{Value: edgeID})
	}
	for key, edgeID := range s.MemoryStore.agentTrustByPair {
		left, right := decodePairIndexKey(key)
		add(pIdxAgentTrustPair, s.objectKey(pIdxAgentTrustPair, escapeKeySegment(left)+"_"+escapeKeySegment(right)+".json"), s3IndexValue{Value: edgeID})
	}

	return desired
}

func (s *s3StateStore) persistedPrefixes() []s3KeyDescriptor {
	return []s3KeyDescriptor{
		{Prefix: s.prefixed("state/humans")},
		{Prefix: s.prefixed("state/agents")},
		{Prefix: s.prefixed("state/orgs")},
		{Prefix: s.prefixed("state/memberships")},
		{Prefix: s.prefixed("state/invites")},
		{Prefix: s.prefixed("state/org_access_keys")},
		{Prefix: s.prefixed("state/binds")},
		{Prefix: s.prefixed("state/org_trusts")},
		{Prefix: s.prefixed("state/agent_trusts")},
		{Prefix: s.prefixed("state/stats")},
		{Prefix: s.prefixed("state/stats_daily")},
		{Prefix: s.prefixed("state/audit")},
		{Prefix: s.prefixed("state/personal_orgs")},
		{Prefix: s.prefixed("idx/humans/by_auth")},
		{Prefix: s.prefixed("idx/humans/by_handle")},
		{Prefix: s.prefixed("idx/orgs/by_handle")},
		{Prefix: s.prefixed("idx/memberships/by_org_human")},
		{Prefix: s.prefixed("idx/invites/by_secret_hash")},
		{Prefix: s.prefixed("idx/org_access_keys/by_token_hash")},
		{Prefix: s.prefixed("idx/agents/by_token_hash")},
		{Prefix: s.prefixed("idx/agents/by_uri")},
		{Prefix: s.prefixed("idx/org_trusts/by_pair")},
		{Prefix: s.prefixed("idx/agent_trusts/by_pair")},
	}
}

func (s *s3StateStore) loadFromS3(ctx context.Context) error {
	loaded := NewMemoryStore()

	pHumans := s.prefixed("state/humans")
	pAgents := s.prefixed("state/agents")
	pOrgs := s.prefixed("state/orgs")
	pMemberships := s.prefixed("state/memberships")
	pInvites := s.prefixed("state/invites")
	pAccessKeys := s.prefixed("state/org_access_keys")
	pBinds := s.prefixed("state/binds")
	pOrgTrusts := s.prefixed("state/org_trusts")
	pAgentTrusts := s.prefixed("state/agent_trusts")
	pStats := s.prefixed("state/stats")
	pStatsDaily := s.prefixed("state/stats_daily")
	pAudit := s.prefixed("state/audit")
	pPersonalOrgs := s.prefixed("state/personal_orgs")

	if err := s.loadTypedObjects(ctx, pHumans, func(key string, body []byte) error {
		var value model.Human
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.HumanID) != "" {
			loaded.humans[value.HumanID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pAgents, func(key string, body []byte) error {
		var value s3PersistAgent
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.AgentUUID) != "" {
			loaded.agents[value.AgentUUID] = value.toModel()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pOrgs, func(key string, body []byte) error {
		var value model.Organization
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.OrgID) != "" {
			loaded.orgs[value.OrgID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pMemberships, func(key string, body []byte) error {
		var value model.Membership
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.MembershipID) != "" {
			loaded.memberships[value.MembershipID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pInvites, func(key string, body []byte) error {
		var value s3PersistInvite
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.InviteID) != "" {
			loaded.invites[value.InviteID] = value.toModel()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pAccessKeys, func(key string, body []byte) error {
		var value s3PersistOrgAccessKey
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.KeyID) != "" {
			loaded.orgAccessKeys[value.KeyID] = value.toModel()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pBinds, func(key string, body []byte) error {
		var value s3PersistBindToken
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.BindID) != "" {
			loaded.binds[value.BindID] = value.toModel()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pOrgTrusts, func(key string, body []byte) error {
		var value model.TrustEdge
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.EdgeID) != "" {
			loaded.orgTrusts[value.EdgeID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pAgentTrusts, func(key string, body []byte) error {
		var value model.TrustEdge
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.EdgeID) != "" {
			loaded.agentTrusts[value.EdgeID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pStats, func(key string, body []byte) error {
		var value model.OrgStats
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.OrgID) != "" {
			loaded.statsByOrg[value.OrgID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pStatsDaily, func(key string, body []byte) error {
		relative, ok := trimObjectKeyPrefix(key, pStatsDaily)
		if !ok {
			return nil
		}
		parts := strings.Split(relative, "/")
		if len(parts) != 2 {
			return nil
		}
		orgID, err := url.PathUnescape(parts[0])
		if err != nil {
			return nil
		}

		var value model.OrgDailyStats
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if loaded.statsDaily[orgID] == nil {
			loaded.statsDaily[orgID] = make(map[string]model.OrgDailyStats)
		}
		loaded.statsDaily[orgID][value.Date] = value
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pAudit, func(key string, body []byte) error {
		relative, ok := trimObjectKeyPrefix(key, pAudit)
		if !ok {
			return nil
		}
		parts := strings.Split(relative, "/")
		if len(parts) != 2 {
			return nil
		}
		orgID, err := url.PathUnescape(parts[0])
		if err != nil {
			return nil
		}
		var value model.AuditEvent
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		loaded.auditByOrg[orgID] = append(loaded.auditByOrg[orgID], value)
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pPersonalOrgs, func(key string, body []byte) error {
		relative, ok := trimObjectKeyPrefix(key, pPersonalOrgs)
		if !ok {
			return nil
		}
		humanID := strings.TrimSuffix(relative, ".json")
		humanID, err := url.PathUnescape(humanID)
		if err != nil {
			return nil
		}
		var value s3PersonalOrgValue
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.OrgID) == "" {
			return nil
		}
		loaded.personalOrgByHuman[humanID] = value.OrgID
		return nil
	}); err != nil {
		return err
	}

	for orgID, events := range loaded.auditByOrg {
		sort.Slice(events, func(i, j int) bool {
			if events[i].CreatedAt.Equal(events[j].CreatedAt) {
				return events[i].EventID < events[j].EventID
			}
			return events[i].CreatedAt.Before(events[j].CreatedAt)
		})
		loaded.auditByOrg[orgID] = events
	}
	rebuildStateIndexesLocked(loaded)

	s.MemoryStore = loaded
	s.persistedObjects = flattenS3Objects(s.buildDesiredObjects())
	return nil
}

func flattenS3Objects(byPrefix map[string]map[string][]byte) map[string][]byte {
	flat := make(map[string][]byte)
	for _, objects := range byPrefix {
		for key, body := range objects {
			flat[key] = append([]byte(nil), body...)
		}
	}
	return flat
}

func cloneS3Objects(objects map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(objects))
	for key, body := range objects {
		out[key] = append([]byte(nil), body...)
	}
	return out
}

func rebuildStateIndexesLocked(mem *MemoryStore) {
	mem.orgByHandle = make(map[string]string)
	mem.humanByAuthKey = make(map[string]string)
	mem.humanByHandle = make(map[string]string)
	mem.membershipByOrgUser = make(map[string]string)
	mem.inviteBySecretHash = make(map[string]string)
	mem.orgAccessKeyByHash = make(map[string]string)
	mem.agentByURI = make(map[string]string)
	mem.agentTokenIdx = make(map[string]string)
	mem.orgOwnedAgentNameIdx = make(map[string]string)
	mem.humanOwnedAgentNameIdx = make(map[string]string)
	mem.bindByHash = make(map[string]string)
	mem.orgTrustByPair = make(map[string]string)
	mem.agentTrustByPair = make(map[string]string)
	if mem.queues == nil {
		mem.queues = make(map[string][]model.Message)
	}

	for orgID, org := range mem.orgs {
		mem.orgByHandle[normalizeOrgHandleKey(org.Handle)] = orgID
	}
	for humanID, human := range mem.humans {
		mem.humanByAuthKey[authKey(human.AuthProvider, human.AuthSubject)] = humanID
		if human.Handle != "" {
			mem.humanByHandle[normalizeHumanHandleCandidate(human.Handle)] = humanID
		}
	}
	for membershipID, membership := range mem.memberships {
		mem.membershipByOrgUser[orgHumanKey(membership.OrgID, membership.HumanID)] = membershipID
	}
	for inviteID, invite := range mem.invites {
		if invite.Status == model.StatusPending && invite.InviteSecret != "" {
			mem.inviteBySecretHash[invite.InviteSecret] = inviteID
		}
	}
	for keyID, key := range mem.orgAccessKeys {
		if key.TokenHash == "" {
			continue
		}
		if key.Status == model.StatusRevoked || key.RevokedAt != nil {
			continue
		}
		mem.orgAccessKeyByHash[key.TokenHash] = keyID
	}
	for agentUUID, agent := range mem.agents {
		if agent.Status == model.StatusRevoked {
			continue
		}
		mem.agentByURI[agent.AgentID] = agentUUID
		mem.agentTokenIdx[agent.TokenHash] = agentUUID
		if agent.OwnerHumanID != nil {
			mem.humanOwnedAgentNameIdx[humanOwnedAgentNameKey(agent.OrgID, *agent.OwnerHumanID, agent.Handle)] = agentUUID
		} else {
			mem.orgOwnedAgentNameIdx[orgOwnedAgentNameKey(agent.OrgID, agent.Handle)] = agentUUID
		}
		mem.queues[agentUUID] = mem.queues[agentUUID]
	}
	for bindID, bind := range mem.binds {
		if bind.TokenHash != "" {
			mem.bindByHash[bind.TokenHash] = bindID
		}
	}
	for edgeID, edge := range mem.orgTrusts {
		mem.orgTrustByPair[pairKey(edge.LeftID, edge.RightID)] = edgeID
	}
	for edgeID, edge := range mem.agentTrusts {
		mem.agentTrustByPair[pairKey(edge.LeftID, edge.RightID)] = edgeID
	}
}

func (s *s3StateStore) loadTypedObjects(ctx context.Context, prefix string, fn func(key string, body []byte) error) error {
	keys, err := s.listKeys(ctx, prefix+"/")
	if err != nil {
		return err
	}
	for _, key := range keys {
		body, found, err := s.getObject(ctx, key)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if err := fn(key, body); err != nil {
			return fmt.Errorf("decode %s: %w", key, err)
		}
	}
	return nil
}

func (s *s3StateStore) listKeys(ctx context.Context, prefix string) ([]string, error) {
	token := ""
	out := make([]string, 0)
	for {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", strings.TrimSpace(prefix))
		if token != "" {
			query.Set("continuation-token", token)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL("", query), nil)
		if err != nil {
			return nil, fmt.Errorf("build list request: %w", err)
		}
		if err := s.signRequest(req, nil); err != nil {
			return nil, err
		}
		resp, err := s.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list objects: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusNotFound {
				return out, nil
			}
			return nil, fmt.Errorf("list objects status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var parsed s3StateListBucketResult
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("decode list result: %w", err)
		}
		for _, item := range parsed.Contents {
			key := strings.TrimSpace(item.Key)
			if key == "" {
				continue
			}
			out = append(out, key)
		}
		if !parsed.IsTruncated || strings.TrimSpace(parsed.NextContinuationToken) == "" {
			break
		}
		token = strings.TrimSpace(parsed.NextContinuationToken)
	}
	sort.Strings(out)
	return out, nil
}

func (s *s3StateStore) putObject(ctx context.Context, key string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key, nil), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build put request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := s.signRequest(req, body); err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	defer resp.Body.Close()
	if !isS3WriteStatus(resp.StatusCode) {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("put object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (s *s3StateStore) getObject(ctx context.Context, key string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key, nil), nil)
	if err != nil {
		return nil, false, fmt.Errorf("build get request: %w", err)
	}
	if err := s.signRequest(req, nil); err != nil {
		return nil, false, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("get object: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, false, fmt.Errorf("get object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, false, fmt.Errorf("read object: %w", err)
	}
	return body, true, nil
}

func (s *s3StateStore) deleteObject(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objectURL(key, nil), nil)
	if err != nil {
		return fmt.Errorf("build delete request: %w", err)
	}
	if err := s.signRequest(req, nil); err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	defer resp.Body.Close()
	if !isS3WriteStatus(resp.StatusCode) {
		if resp.StatusCode == http.StatusNotFound {
			return nil
		}
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delete object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (s *s3StateStore) objectURL(key string, query url.Values) string {
	u, _ := url.Parse(s.endpoint)
	if s.pathStyle {
		p := path.Join("/", s.bucket)
		if strings.TrimSpace(key) != "" {
			p = path.Join(p, escapeS3Path(key))
		}
		u.Path = p
	} else {
		u.Path = path.Join("/", escapeS3Path(key))
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func (s *s3StateStore) signRequest(req *http.Request, payload []byte) error {
	if s.signer == nil {
		return nil
	}
	if err := s.signer.Sign(req, payload, time.Now().UTC()); err != nil {
		return fmt.Errorf("sign request: %w", err)
	}
	return nil
}

func (s *s3StateStore) prefixed(parts ...string) string {
	all := make([]string, 0, len(parts)+1)
	all = append(all, strings.Trim(s.prefix, "/"))
	for _, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		all = append(all, part)
	}
	return strings.Join(all, "/")
}

func (s *s3StateStore) objectKey(parts ...string) string {
	return strings.Join(parts, "/")
}

func trimObjectKeyPrefix(key, prefix string) (string, bool) {
	withSlash := strings.Trim(prefix, "/") + "/"
	key = strings.Trim(key, "/")
	if !strings.HasPrefix(key, withSlash) {
		return "", false
	}
	return strings.TrimPrefix(key, withSlash), true
}

func escapeKeySegment(value string) string {
	return url.PathEscape(strings.TrimSpace(value))
}

func hashKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func decodePairIndexKey(pair string) (string, string) {
	parts := strings.SplitN(pair, "\x00", 2)
	if len(parts) != 2 {
		return pair, ""
	}
	return parts[0], parts[1]
}

func persistInvite(v model.Invite) s3PersistInvite {
	return s3PersistInvite{
		InviteID:     v.InviteID,
		OrgID:        v.OrgID,
		Email:        v.Email,
		Role:         v.Role,
		Status:       v.Status,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    v.CreatedAt,
		ExpiresAt:    v.ExpiresAt,
		AcceptedAt:   v.AcceptedAt,
		RevokedAt:    v.RevokedAt,
		InviteSecret: v.InviteSecret,
	}
}

func (v s3PersistInvite) toModel() model.Invite {
	return model.Invite{
		InviteID:     v.InviteID,
		OrgID:        v.OrgID,
		Email:        v.Email,
		Role:         v.Role,
		Status:       v.Status,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    v.CreatedAt,
		ExpiresAt:    v.ExpiresAt,
		AcceptedAt:   v.AcceptedAt,
		RevokedAt:    v.RevokedAt,
		InviteSecret: v.InviteSecret,
	}
}

func persistAgent(v model.Agent) s3PersistAgent {
	return s3PersistAgent{
		AgentUUID:    v.AgentUUID,
		AgentID:      v.AgentID,
		Handle:       v.Handle,
		OrgID:        v.OrgID,
		OwnerHumanID: v.OwnerHumanID,
		TokenHash:    v.TokenHash,
		Status:       v.Status,
		IsPublic:     v.IsPublic,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    v.CreatedAt,
		RevokedAt:    v.RevokedAt,
	}
}

func (v s3PersistAgent) toModel() model.Agent {
	return model.Agent{
		AgentUUID:    v.AgentUUID,
		AgentID:      v.AgentID,
		Handle:       v.Handle,
		OrgID:        v.OrgID,
		OwnerHumanID: v.OwnerHumanID,
		TokenHash:    v.TokenHash,
		Status:       v.Status,
		IsPublic:     v.IsPublic,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    v.CreatedAt,
		RevokedAt:    v.RevokedAt,
	}
}

func persistBindToken(v model.BindToken) s3PersistBindToken {
	return s3PersistBindToken{
		BindID:       v.BindID,
		OrgID:        v.OrgID,
		OwnerHumanID: v.OwnerHumanID,
		TokenHash:    v.TokenHash,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    v.CreatedAt,
		ExpiresAt:    v.ExpiresAt,
		UsedAt:       v.UsedAt,
	}
}

func (v s3PersistBindToken) toModel() model.BindToken {
	return model.BindToken{
		BindID:       v.BindID,
		OrgID:        v.OrgID,
		OwnerHumanID: v.OwnerHumanID,
		TokenHash:    v.TokenHash,
		CreatedBy:    v.CreatedBy,
		CreatedAt:    v.CreatedAt,
		ExpiresAt:    v.ExpiresAt,
		UsedAt:       v.UsedAt,
	}
}

func persistOrgAccessKey(v model.OrgAccessKey) s3PersistOrgAccessKey {
	return s3PersistOrgAccessKey{
		KeyID:      v.KeyID,
		OrgID:      v.OrgID,
		Label:      v.Label,
		Scopes:     append([]string(nil), v.Scopes...),
		Status:     v.Status,
		CreatedBy:  v.CreatedBy,
		CreatedAt:  v.CreatedAt,
		ExpiresAt:  v.ExpiresAt,
		LastUsedAt: v.LastUsedAt,
		RevokedAt:  v.RevokedAt,
		TokenHash:  v.TokenHash,
	}
}

func (v s3PersistOrgAccessKey) toModel() model.OrgAccessKey {
	return model.OrgAccessKey{
		KeyID:      v.KeyID,
		OrgID:      v.OrgID,
		Label:      v.Label,
		Scopes:     append([]string(nil), v.Scopes...),
		Status:     v.Status,
		CreatedBy:  v.CreatedBy,
		CreatedAt:  v.CreatedAt,
		ExpiresAt:  v.ExpiresAt,
		LastUsedAt: v.LastUsedAt,
		RevokedAt:  v.RevokedAt,
		TokenHash:  v.TokenHash,
	}
}
