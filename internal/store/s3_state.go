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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"moltenhub/internal/model"
)

const (
	defaultS3StateRegion = "us-east-1"
	defaultS3StatePrefix = "moltenhub-state"
	// Cap each state PUT/DELETE call when no caller deadline is provided.
	defaultS3StatePersistTimeout = 8 * time.Second
	// Best-effort metrics/status writes should not block request paths for long.
	defaultS3StateBestEffortPersistTimeout = 750 * time.Millisecond
	// Startup check should fail fast so readiness decisions are not delayed too long.
	defaultS3StateStartupCheckTimeout = 5 * time.Second
	defaultS3StateHydrationTimeout         = 20 * time.Second
	defaultS3StateListConcurrency          = 6
	defaultS3StateGetConcurrency           = 24
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
	// persistTimeout bounds one state write/delete request when the caller context has no deadline.
	persistTimeout time.Duration
	// bestEffortPersistTimeout bounds persist for fire-and-forget style writes.
	bestEffortPersistTimeout time.Duration
	hydrationTimeout         time.Duration
	listConcurrency          int
	getConcurrency           int

	persistMu sync.Mutex
	// persistedObjects tracks the last successfully persisted object bodies by key.
	// It lets us write only changed keys instead of full-prefix rewrites.
	persistedObjects map[string][]byte
	// hydrationPrefetch is populated only while loadFromS3 is running to avoid repeated list/get calls.
	hydrationPrefetch map[string]map[string][]byte
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

type s3PersistQueueMessage struct {
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

type s3PersistMessageRecord struct {
	Message           s3PersistQueueMessage `json:"message"`
	Status            string                `json:"status"`
	AcceptedAt        time.Time             `json:"accepted_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
	FirstReceivedAt   *time.Time            `json:"first_received_at,omitempty"`
	LastLeasedAt      *time.Time            `json:"last_leased_at,omitempty"`
	LeaseExpiresAt    *time.Time            `json:"lease_expires_at,omitempty"`
	AckedAt           *time.Time            `json:"acked_at,omitempty"`
	LastDeliveryID    *string               `json:"last_delivery_id,omitempty"`
	DeliveryAttempts  int                   `json:"delivery_attempts"`
	RequeueCount      int                   `json:"requeue_count"`
	IdempotentReplays int                   `json:"idempotent_replays"`
	LastFailureReason string                `json:"last_failure_reason,omitempty"`
	LastFailureAt     *time.Time            `json:"last_failure_at,omitempty"`
}

type s3PersistMessageDelivery struct {
	DeliveryID     string    `json:"delivery_id"`
	MessageID      string    `json:"message_id"`
	AgentUUID      string    `json:"agent_uuid"`
	Attempt        int       `json:"attempt"`
	LeasedAt       time.Time `json:"leased_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
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
	AgentUUID         string         `json:"agent_uuid"`
	AgentID           string         `json:"agent_id"`
	Handle            string         `json:"handle"`
	HandleFinalizedAt *time.Time     `json:"handle_finalized_at,omitempty"`
	OrgID             string         `json:"org_id"`
	OwnerHumanID      *string        `json:"owner_human_id,omitempty"`
	TokenHash         string         `json:"token_hash"`
	Status            string         `json:"status"`
	Metadata          map[string]any `json:"metadata"`
	CreatedBy         string         `json:"created_by"`
	CreatedAt         time.Time      `json:"created_at"`
	RevokedAt         *time.Time     `json:"revoked_at,omitempty"`
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
	endpoint := strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_ENDPOINT"))
	bucket := strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_BUCKET"))
	region := strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_REGION"))
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_PREFIX")), "/")
	pathStyleRaw := strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_PATH_STYLE"))
	accessKeyID := strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(os.Getenv("MOLTENHUB_STATE_S3_SECRET_ACCESS_KEY"))
	hydrationTimeout := parseDurationSecondsEnv("MOLTENHUB_S3_HYDRATION_TIMEOUT_SEC", defaultS3StateHydrationTimeout)
	listConcurrency := parsePositiveIntEnv("MOLTENHUB_S3_HYDRATION_LIST_CONCURRENCY", defaultS3StateListConcurrency)
	getConcurrency := parsePositiveIntEnv("MOLTENHUB_S3_HYDRATION_GET_CONCURRENCY", defaultS3StateGetConcurrency)

	if endpoint == "" {
		return nil, fmt.Errorf("MOLTENHUB_STATE_S3_ENDPOINT is required for s3 state backend")
	}
	if bucket == "" {
		return nil, fmt.Errorf("MOLTENHUB_STATE_S3_BUCKET is required for s3 state backend")
	}
	if region == "" {
		region = defaultS3StateRegion
	}
	if prefix == "" {
		prefix = defaultS3StatePrefix
	}
	if (accessKeyID == "") != (secretAccessKey == "") {
		return nil, fmt.Errorf("MOLTENHUB_STATE_S3_ACCESS_KEY_ID and MOLTENHUB_STATE_S3_SECRET_ACCESS_KEY must be set together")
	}
	pathStyle := true
	if pathStyleRaw != "" {
		pathStyle = strings.EqualFold(pathStyleRaw, "true")
	}
	if !pathStyle {
		return nil, fmt.Errorf("MOLTENHUB_STATE_S3_PATH_STYLE=false is not supported in this build")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("MOLTENHUB_STATE_S3_ENDPOINT must include http:// or https:// scheme")
	}

	store := &s3StateStore{
		MemoryStore:              NewMemoryStore(),
		httpClient:               newS3HTTPClient(10 * time.Second),
		endpoint:                 strings.TrimSuffix(endpoint, "/"),
		bucket:                   bucket,
		region:                   region,
		prefix:                   prefix,
		pathStyle:                pathStyle,
		signer:                   newS3Signer(accessKeyID, secretAccessKey, region),
		persistTimeout:           defaultS3StatePersistTimeout,
		bestEffortPersistTimeout: defaultS3StateBestEffortPersistTimeout,
		hydrationTimeout:         hydrationTimeout,
		listConcurrency:          listConcurrency,
		getConcurrency:           getConcurrency,
	}
	loadCtx := context.Background()
	if store.hydrationTimeout > 0 {
		var cancel context.CancelFunc
		loadCtx, cancel = context.WithTimeout(loadCtx, store.hydrationTimeout)
		defer cancel()
	}
	if err := store.loadFromS3(loadCtx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *s3StateStore) StartupCheck(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultS3StateStartupCheckTimeout)
		defer cancel()
	}

	probeKey := s.objectKey(
		s.prefixed("startup-check"),
		fmt.Sprintf("%019d.json", time.Now().UTC().UnixNano()),
	)
	body := []byte(`{"check":"state_startup"}`)
	if err := s.putObject(ctx, probeKey, body); err != nil {
		return fmt.Errorf("state startup check write failed: %w", err)
	}
	if err := s.deleteObject(ctx, probeKey); err != nil {
		return fmt.Errorf("state startup check cleanup failed: %w", err)
	}
	return nil
}

func parseDurationSecondsEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	seconds, err := time.ParseDuration(raw + "s")
	if err != nil || seconds <= 0 {
		return fallback
	}
	return seconds
}

func parsePositiveIntEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
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
	if existingFound && reflect.DeepEqual(existing, human) {
		return human, nil
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Human{}, err
	}
	return human, nil
}

func (s *s3StateStore) UpdateHumanProfile(humanID, handle string, confirmHandle bool, now time.Time) (model.Human, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	human, err := s.MemoryStore.UpdateHumanProfile(humanID, handle, confirmHandle, now)
	if err != nil {
		return model.Human{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Human{}, err
	}
	return human, nil
}

func (s *s3StateStore) UpdateHumanMetadata(humanID string, metadata map[string]any, now time.Time) (model.Human, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	human, err := s.MemoryStore.UpdateHumanMetadata(humanID, metadata, now)
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

func (s *s3StateStore) DeleteOrg(orgID, actorHumanID string, isSuperAdmin bool, now time.Time) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if err := s.MemoryStore.DeleteOrg(orgID, actorHumanID, isSuperAdmin, now); err != nil {
		return err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return err
	}
	return nil
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

func (s *s3StateStore) UpdateOrgMetadata(orgID string, metadata map[string]any, actorHumanID string, isSuperAdmin bool, now time.Time) (model.Organization, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	org, err := s.MemoryStore.UpdateOrgMetadata(orgID, metadata, actorHumanID, isSuperAdmin, now)
	if err != nil {
		return model.Organization{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Organization{}, err
	}
	return org, nil
}

func (s *s3StateStore) UpdateAgentMetadata(agentUUID string, metadata map[string]any, actorHumanID string, now time.Time, isSuperAdmin bool) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.UpdateAgentMetadata(agentUUID, metadata, actorHumanID, now, isSuperAdmin)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) UpdateAgentMetadataSelf(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.UpdateAgentMetadataSelf(agentUUID, metadata, now)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) UpdateAgentMetadataSelfBestEffort(agentUUID string, metadata map[string]any, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.UpdateAgentMetadataSelf(agentUUID, metadata, now)
	if err != nil {
		return model.Agent{}, err
	}
	s.persistAllBestEffortLocked()
	return agent, nil
}

func (s *s3StateStore) FinalizeAgentHandleSelf(agentUUID, handle string, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.FinalizeAgentHandleSelf(agentUUID, handle, now)
	if err != nil {
		return model.Agent{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Agent{}, err
	}
	return agent, nil
}

func (s *s3StateStore) FinalizeAgentHandleSelfBestEffort(agentUUID, handle string, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.FinalizeAgentHandleSelf(agentUUID, handle, now)
	if err != nil {
		return model.Agent{}, err
	}
	s.persistAllBestEffortLocked()
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
	s.persistAllBestEffortLocked()
}

func (s *s3StateStore) RecordAgentSystemActivity(agentUUID string, entry map[string]any, now time.Time) (model.Agent, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	agent, err := s.MemoryStore.RecordAgentSystemActivity(agentUUID, entry, now)
	if err != nil {
		return model.Agent{}, err
	}
	s.persistAllBestEffortLocked()
	return agent, nil
}

func (s *s3StateStore) RecordMessageDropped(orgID string) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.MemoryStore.RecordMessageDropped(orgID)
	s.persistAllBestEffortLocked()
}

func (s *s3StateStore) persistAllBestEffortLocked() {
	timeout := s.bestEffortPersistTimeout
	if timeout <= 0 {
		timeout = defaultS3StateBestEffortPersistTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_ = s.persistAll(ctx)
}

func (s *s3StateStore) persistAll(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

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
		opCtx, cancel := s.persistOperationContext(ctx)
		err := s.putObject(opCtx, key, desired[key])
		cancel()
		if err != nil {
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
		opCtx, cancel := s.persistOperationContext(ctx)
		err := s.deleteObject(opCtx, key)
		cancel()
		if err != nil {
			return err
		}
	}

	s.persistedObjects = cloneS3Objects(desired)
	return nil
}

func (s *s3StateStore) persistOperationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	timeout := s.persistTimeout
	if timeout <= 0 {
		timeout = defaultS3StatePersistTimeout
	}
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
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
	pQueues := s.prefixed("state/queues")
	pMessages := s.prefixed("state/messages")
	pMessageLeases := s.prefixed("state/message_leases")
	pPeers := s.prefixed("state/peers")
	pRemoteOrgTrusts := s.prefixed("state/remote_org_trusts")
	pRemoteAgentTrusts := s.prefixed("state/remote_agent_trusts")
	pPeerOutbounds := s.prefixed("state/peer_outbounds")

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
	for agentUUID, queue := range s.MemoryStore.queues {
		for _, message := range queue {
			add(
				pQueues,
				s.objectKey(pQueues, escapeKeySegment(agentUUID), queueMessageObjectName(message)),
				persistQueueMessage(message),
			)
		}
	}
	for messageID, record := range s.MemoryStore.messageRecords {
		add(pMessages, s.objectKey(pMessages, escapeKeySegment(messageID)+".json"), persistMessageRecord(record))
	}
	for deliveryID, delivery := range s.MemoryStore.messageDeliveries {
		add(pMessageLeases, s.objectKey(pMessageLeases, escapeKeySegment(deliveryID)+".json"), persistMessageDelivery(delivery))
	}
	for peerID, peer := range s.MemoryStore.peerInstances {
		add(pPeers, s.objectKey(pPeers, escapeKeySegment(peerID)+".json"), peer)
	}
	for trustID, trust := range s.MemoryStore.remoteOrgTrusts {
		add(pRemoteOrgTrusts, s.objectKey(pRemoteOrgTrusts, escapeKeySegment(trustID)+".json"), trust)
	}
	for trustID, trust := range s.MemoryStore.remoteAgentTrusts {
		add(pRemoteAgentTrusts, s.objectKey(pRemoteAgentTrusts, escapeKeySegment(trustID)+".json"), trust)
	}
	for outboundID, outbound := range s.MemoryStore.peerOutbounds {
		add(pPeerOutbounds, s.objectKey(pPeerOutbounds, escapeKeySegment(outboundID)+".json"), outbound)
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
	pIdxMessageClient := s.prefixed("idx/messages/by_client")
	pIdxPeerCanonical := s.prefixed("idx/peers/by_canonical_base")
	pIdxRemoteOrgTrust := s.prefixed("idx/remote_org_trusts/by_key")
	pIdxRemoteAgentTrust := s.prefixed("idx/remote_agent_trusts/by_key")

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
	for key, messageID := range s.MemoryStore.messageByClientMsg {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 {
			continue
		}
		add(pIdxMessageClient, s.objectKey(pIdxMessageClient, escapeKeySegment(parts[0]), escapeKeySegment(parts[1])+".json"), s3IndexValue{Value: messageID})
	}
	for canonicalBase, peerID := range s.MemoryStore.peerByCanonicalBase {
		add(pIdxPeerCanonical, s.objectKey(pIdxPeerCanonical, hashKey(canonicalBase)+".json"), s3IndexValue{Value: peerID})
	}
	for key, trustID := range s.MemoryStore.remoteOrgTrustByKey {
		add(pIdxRemoteOrgTrust, s.objectKey(pIdxRemoteOrgTrust, hashKey(key)+".json"), s3IndexValue{Value: trustID})
	}
	for key, trustID := range s.MemoryStore.remoteAgentTrustByKey {
		add(pIdxRemoteAgentTrust, s.objectKey(pIdxRemoteAgentTrust, hashKey(key)+".json"), s3IndexValue{Value: trustID})
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
		{Prefix: s.prefixed("state/queues")},
		{Prefix: s.prefixed("state/messages")},
		{Prefix: s.prefixed("state/message_leases")},
		{Prefix: s.prefixed("state/peers")},
		{Prefix: s.prefixed("state/remote_org_trusts")},
		{Prefix: s.prefixed("state/remote_agent_trusts")},
		{Prefix: s.prefixed("state/peer_outbounds")},
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
		{Prefix: s.prefixed("idx/messages/by_client")},
		{Prefix: s.prefixed("idx/peers/by_canonical_base")},
		{Prefix: s.prefixed("idx/remote_org_trusts/by_key")},
		{Prefix: s.prefixed("idx/remote_agent_trusts/by_key")},
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
	pQueues := s.prefixed("state/queues")
	pMessages := s.prefixed("state/messages")
	pMessageLeases := s.prefixed("state/message_leases")
	pPeers := s.prefixed("state/peers")
	pRemoteOrgTrusts := s.prefixed("state/remote_org_trusts")
	pRemoteAgentTrusts := s.prefixed("state/remote_agent_trusts")
	pPeerOutbounds := s.prefixed("state/peer_outbounds")
	prefetched, err := s.prefetchPrefixes(ctx, []string{
		pHumans,
		pAgents,
		pOrgs,
		pMemberships,
		pInvites,
		pAccessKeys,
		pBinds,
		pOrgTrusts,
		pAgentTrusts,
		pStats,
		pStatsDaily,
		pAudit,
		pPersonalOrgs,
		pQueues,
		pMessages,
		pMessageLeases,
		pPeers,
		pRemoteOrgTrusts,
		pRemoteAgentTrusts,
		pPeerOutbounds,
	})
	if err != nil {
		return err
	}
	s.hydrationPrefetch = prefetched
	defer func() { s.hydrationPrefetch = nil }()

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
	if err := s.loadTypedObjects(ctx, pQueues, func(key string, body []byte) error {
		relative, ok := trimObjectKeyPrefix(key, pQueues)
		if !ok {
			return nil
		}
		parts := strings.Split(relative, "/")
		if len(parts) != 2 {
			return nil
		}
		agentUUID, err := url.PathUnescape(parts[0])
		if err != nil || strings.TrimSpace(agentUUID) == "" {
			return nil
		}
		var value s3PersistQueueMessage
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		loaded.queues[agentUUID] = append(loaded.queues[agentUUID], value.toModel())
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pMessages, func(key string, body []byte) error {
		var value s3PersistMessageRecord
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		record := value.toModel()
		if strings.TrimSpace(record.Message.MessageID) != "" {
			loaded.messageRecords[record.Message.MessageID] = record
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pMessageLeases, func(key string, body []byte) error {
		var value s3PersistMessageDelivery
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		delivery := value.toModel()
		if strings.TrimSpace(delivery.DeliveryID) != "" {
			loaded.messageDeliveries[delivery.DeliveryID] = delivery
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pPeers, func(key string, body []byte) error {
		var value model.PeerInstance
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.PeerID) != "" {
			loaded.peerInstances[value.PeerID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pRemoteOrgTrusts, func(key string, body []byte) error {
		var value model.RemoteOrgTrust
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.TrustID) != "" {
			loaded.remoteOrgTrusts[value.TrustID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pRemoteAgentTrusts, func(key string, body []byte) error {
		var value model.RemoteAgentTrust
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.TrustID) != "" {
			loaded.remoteAgentTrusts[value.TrustID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.loadTypedObjects(ctx, pPeerOutbounds, func(key string, body []byte) error {
		var value model.PeerOutboundMessage
		if err := json.Unmarshal(body, &value); err != nil {
			return err
		}
		if strings.TrimSpace(value.OutboundID) != "" {
			loaded.peerOutbounds[value.OutboundID] = value
		}
		return nil
	}); err != nil {
		return err
	}
	for agentUUID, queue := range loaded.queues {
		sort.Slice(queue, func(i, j int) bool {
			if queue[i].CreatedAt.Equal(queue[j].CreatedAt) {
				return queue[i].MessageID < queue[j].MessageID
			}
			return queue[i].CreatedAt.Before(queue[j].CreatedAt)
		})
		loaded.queues[agentUUID] = queue
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
	mem.messageByClientMsg = make(map[string]string)
	mem.peerByCanonicalBase = make(map[string]string)
	mem.remoteOrgTrustByKey = make(map[string]string)
	mem.remoteAgentTrustByKey = make(map[string]string)
	mem.peerOutboundByPeer = make(map[string][]string)
	if mem.queues == nil {
		mem.queues = make(map[string][]model.Message)
	}
	if mem.messageRecords == nil {
		mem.messageRecords = make(map[string]model.MessageRecord)
	}
	if mem.messageDeliveries == nil {
		mem.messageDeliveries = make(map[string]model.MessageDelivery)
	}
	if mem.peerInstances == nil {
		mem.peerInstances = make(map[string]model.PeerInstance)
	}
	if mem.remoteOrgTrusts == nil {
		mem.remoteOrgTrusts = make(map[string]model.RemoteOrgTrust)
	}
	if mem.remoteAgentTrusts == nil {
		mem.remoteAgentTrusts = make(map[string]model.RemoteAgentTrust)
	}
	if mem.peerOutbounds == nil {
		mem.peerOutbounds = make(map[string]model.PeerOutboundMessage)
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
		normalizedMetadata, err := validateAndNormalizeAgentMetadata(agent.Metadata)
		if err != nil {
			normalizedMetadata = defaultAgentMetadata()
		}
		agent.Metadata = normalizedMetadata
		mem.agents[agentUUID] = agent

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
	for messageID, record := range mem.messageRecords {
		if record.Message.ClientMsgID == nil {
			continue
		}
		mem.messageByClientMsg[messageClientKey(record.Message.FromAgentUUID, *record.Message.ClientMsgID)] = messageID
	}
	for peerID, peer := range mem.peerInstances {
		mem.peerByCanonicalBase[normalizeBaseURL(peer.CanonicalBaseURL)] = peerID
	}
	for trustID, trust := range mem.remoteOrgTrusts {
		mem.remoteOrgTrustByKey[remoteOrgTrustKey(trust.LocalOrgID, trust.PeerID, trust.RemoteOrgHandle)] = trustID
	}
	for trustID, trust := range mem.remoteAgentTrusts {
		mem.remoteAgentTrustByKey[remoteAgentTrustKey(trust.LocalAgentUUID, trust.PeerID, trust.RemoteAgentURI)] = trustID
	}
	for outboundID, outbound := range mem.peerOutbounds {
		mem.peerOutboundByPeer[outbound.PeerID] = append(mem.peerOutboundByPeer[outbound.PeerID], outboundID)
	}
}

func (s *s3StateStore) prefetchPrefixes(ctx context.Context, prefixes []string) (map[string]map[string][]byte, error) {
	if len(prefixes) == 0 {
		return map[string]map[string][]byte{}, nil
	}
	listConcurrency := s.listConcurrency
	if listConcurrency <= 0 {
		listConcurrency = defaultS3StateListConcurrency
	}
	getConcurrency := s.getConcurrency
	if getConcurrency <= 0 {
		getConcurrency = defaultS3StateGetConcurrency
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	setErr := func(err error, firstErr *error, mu *sync.Mutex) {
		if err == nil {
			return
		}
		mu.Lock()
		if *firstErr == nil {
			*firstErr = err
			cancel()
		}
		mu.Unlock()
	}

	keysByPrefix := make(map[string][]string, len(prefixes))
	var (
		firstErr error
		errMu    sync.Mutex
		keysMu   sync.Mutex
		listWG   sync.WaitGroup
	)
	prefixJobs := make(chan string)
	for i := 0; i < listConcurrency; i++ {
		listWG.Add(1)
		go func() {
			defer listWG.Done()
			for prefix := range prefixJobs {
				if ctx.Err() != nil {
					return
				}
				keys, err := s.listKeys(ctx, prefix+"/")
				if err != nil {
					setErr(fmt.Errorf("list objects %s: %w", prefix, err), &firstErr, &errMu)
					continue
				}
				keysMu.Lock()
				keysByPrefix[prefix] = keys
				keysMu.Unlock()
			}
		}()
	}
prefixLoop:
	for _, prefix := range prefixes {
		select {
		case prefixJobs <- prefix:
		case <-ctx.Done():
			break prefixLoop
		}
	}
	close(prefixJobs)
	listWG.Wait()
	if firstErr != nil {
		return nil, firstErr
	}

	objectsByPrefix := make(map[string]map[string][]byte, len(prefixes))
	for _, prefix := range prefixes {
		objectsByPrefix[prefix] = make(map[string][]byte)
	}
	type getJob struct {
		prefix string
		key    string
	}
	getJobs := make(chan getJob)
	var (
		getWG sync.WaitGroup
		objMu sync.Mutex
	)
	for i := 0; i < getConcurrency; i++ {
		getWG.Add(1)
		go func() {
			defer getWG.Done()
			for job := range getJobs {
				if ctx.Err() != nil {
					return
				}
				body, found, err := s.getObject(ctx, job.key)
				if err != nil {
					setErr(fmt.Errorf("get object %s: %w", job.key, err), &firstErr, &errMu)
					continue
				}
				if !found {
					continue
				}
				objMu.Lock()
				objectsByPrefix[job.prefix][job.key] = body
				objMu.Unlock()
			}
		}()
	}
getLoop:
	for _, prefix := range prefixes {
		keys := keysByPrefix[prefix]
		for _, key := range keys {
			select {
			case getJobs <- getJob{prefix: prefix, key: key}:
			case <-ctx.Done():
				break getLoop
			}
		}
	}
	close(getJobs)
	getWG.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return objectsByPrefix, nil
}

func (s *s3StateStore) loadTypedObjects(ctx context.Context, prefix string, fn func(key string, body []byte) error) error {
	if prefetched := s.hydrationPrefetch; prefetched != nil {
		objects := prefetched[prefix]
		keys := make([]string, 0, len(objects))
		for key := range objects {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := fn(key, objects[key]); err != nil {
				return fmt.Errorf("decode %s: %w", key, err)
			}
		}
		return nil
	}
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

func (s *s3StateStore) CreatePeerInstance(canonicalBaseURL, deliveryBaseURL, sharedSecret, actorHumanID, peerID string, now time.Time) (model.PeerInstance, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	peer, err := s.MemoryStore.CreatePeerInstance(canonicalBaseURL, deliveryBaseURL, sharedSecret, actorHumanID, peerID, now)
	if err != nil {
		return model.PeerInstance{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.PeerInstance{}, err
	}
	return peer, nil
}

func (s *s3StateStore) DeletePeerInstance(peerID, actorHumanID string, now time.Time) (model.PeerInstance, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	peer, err := s.MemoryStore.DeletePeerInstance(peerID, actorHumanID, now)
	if err != nil {
		return model.PeerInstance{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.PeerInstance{}, err
	}
	return peer, nil
}

func (s *s3StateStore) RecordPeerDeliverySuccess(peerID string, now time.Time) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.MemoryStore.RecordPeerDeliverySuccess(peerID, now)
	s.persistAllBestEffortLocked()
}

func (s *s3StateStore) RecordPeerDeliveryFailure(peerID, reason string, now time.Time) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.MemoryStore.RecordPeerDeliveryFailure(peerID, reason, now)
	s.persistAllBestEffortLocked()
}

func (s *s3StateStore) CreateRemoteOrgTrust(localOrgID, peerID, remoteOrgHandle, actorHumanID, trustID string, now time.Time) (model.RemoteOrgTrust, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	trust, err := s.MemoryStore.CreateRemoteOrgTrust(localOrgID, peerID, remoteOrgHandle, actorHumanID, trustID, now)
	if err != nil {
		return model.RemoteOrgTrust{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.RemoteOrgTrust{}, err
	}
	return trust, nil
}

func (s *s3StateStore) DeleteRemoteOrgTrust(trustID, actorHumanID string, now time.Time) (model.RemoteOrgTrust, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	trust, err := s.MemoryStore.DeleteRemoteOrgTrust(trustID, actorHumanID, now)
	if err != nil {
		return model.RemoteOrgTrust{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.RemoteOrgTrust{}, err
	}
	return trust, nil
}

func (s *s3StateStore) CreateRemoteAgentTrust(localAgentUUID, peerID, remoteAgentURI, actorHumanID, trustID string, now time.Time) (model.RemoteAgentTrust, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	trust, err := s.MemoryStore.CreateRemoteAgentTrust(localAgentUUID, peerID, remoteAgentURI, actorHumanID, trustID, now)
	if err != nil {
		return model.RemoteAgentTrust{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.RemoteAgentTrust{}, err
	}
	return trust, nil
}

func (s *s3StateStore) DeleteRemoteAgentTrust(trustID, actorHumanID string, now time.Time) (model.RemoteAgentTrust, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	trust, err := s.MemoryStore.DeleteRemoteAgentTrust(trustID, actorHumanID, now)
	if err != nil {
		return model.RemoteAgentTrust{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.RemoteAgentTrust{}, err
	}
	return trust, nil
}

func (s *s3StateStore) EnqueuePeerOutbound(peerID, outboundID string, message model.Message, now time.Time) (model.PeerOutboundMessage, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	outbound, err := s.MemoryStore.EnqueuePeerOutbound(peerID, outboundID, message, now)
	if err != nil {
		return model.PeerOutboundMessage{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.PeerOutboundMessage{}, err
	}
	return outbound, nil
}

func (s *s3StateStore) MarkPeerOutboundRetry(outboundID, reason string, nextAttemptAt, now time.Time) (model.PeerOutboundMessage, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	outbound, err := s.MemoryStore.MarkPeerOutboundRetry(outboundID, reason, nextAttemptAt, now)
	if err != nil {
		return model.PeerOutboundMessage{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.PeerOutboundMessage{}, err
	}
	return outbound, nil
}

func (s *s3StateStore) MarkPeerOutboundDelivered(outboundID string, now time.Time) (model.PeerOutboundMessage, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	outbound, err := s.MemoryStore.MarkPeerOutboundDelivered(outboundID, now)
	if err != nil {
		return model.PeerOutboundMessage{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.PeerOutboundMessage{}, err
	}
	return outbound, nil
}

func (s *s3StateStore) DeleteAgent(agentUUID, actorHumanID string, now time.Time, isSuperAdmin bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if err := s.MemoryStore.DeleteAgent(agentUUID, actorHumanID, now, isSuperAdmin); err != nil {
		return err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return err
	}
	return nil
}

func (s *s3StateStore) CreateOrGetMessageRecord(message model.Message, acceptedAt time.Time) (model.MessageRecord, bool, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	record, replay, err := s.MemoryStore.CreateOrGetMessageRecord(message, acceptedAt)
	if err != nil {
		return model.MessageRecord{}, false, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.MessageRecord{}, false, err
	}
	return record, replay, nil
}

func (s *s3StateStore) MarkMessageForwarded(messageID string, forwardedAt time.Time) (model.MessageRecord, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	record, err := s.MemoryStore.MarkMessageForwarded(messageID, forwardedAt)
	if err != nil {
		return model.MessageRecord{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.MessageRecord{}, err
	}
	return record, nil
}

func (s *s3StateStore) AbortMessageRecord(messageID string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if err := s.MemoryStore.AbortMessageRecord(messageID); err != nil {
		return err
	}
	return s.persistAll(context.Background())
}

func (s *s3StateStore) GetMessageRecord(messageID string) (model.MessageRecord, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	return s.MemoryStore.GetMessageRecord(messageID)
}

func (s *s3StateStore) LeaseMessage(messageID, receiverAgentUUID, deliveryID string, leasedAt, leaseExpiresAt time.Time) (model.MessageDelivery, model.MessageRecord, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	delivery, record, err := s.MemoryStore.LeaseMessage(messageID, receiverAgentUUID, deliveryID, leasedAt, leaseExpiresAt)
	if err != nil {
		return model.MessageDelivery{}, model.MessageRecord{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.MessageDelivery{}, model.MessageRecord{}, err
	}
	return delivery, record, nil
}

func (s *s3StateStore) AckMessageDelivery(receiverAgentUUID, deliveryID string, ackedAt time.Time) (model.MessageRecord, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	record, err := s.MemoryStore.AckMessageDelivery(receiverAgentUUID, deliveryID, ackedAt)
	if err != nil {
		return model.MessageRecord{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.MessageRecord{}, err
	}
	return record, nil
}

func (s *s3StateStore) ReleaseMessageDelivery(receiverAgentUUID, deliveryID string, now time.Time, reason string) (model.Message, model.MessageRecord, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	message, record, err := s.MemoryStore.ReleaseMessageDelivery(receiverAgentUUID, deliveryID, now, reason)
	if err != nil {
		return model.Message{}, model.MessageRecord{}, err
	}
	if err := s.persistAll(context.Background()); err != nil {
		return model.Message{}, model.MessageRecord{}, err
	}
	return message, record, nil
}

func (s *s3StateStore) ExpireMessageLeases(now time.Time) ([]model.Message, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	messages, err := s.MemoryStore.ExpireMessageLeases(now)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, nil
	}
	if err := s.persistAll(context.Background()); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *s3StateStore) GetQueueMetrics() model.QueueMetrics {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	return s.MemoryStore.GetQueueMetrics()
}

func (s *s3StateStore) Enqueue(ctx context.Context, message model.Message) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	if err := s.MemoryStore.Enqueue(ctx, message); err != nil {
		return err
	}
	return s.persistAll(ctx)
}

func (s *s3StateStore) Dequeue(ctx context.Context, agentUUID string) (model.Message, bool, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	message, ok, err := s.MemoryStore.Dequeue(ctx, agentUUID)
	if err != nil || !ok {
		return message, ok, err
	}
	if err := s.persistAll(ctx); err != nil {
		return model.Message{}, false, err
	}
	return message, true, nil
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

func persistQueueMessage(v model.Message) s3PersistQueueMessage {
	return s3PersistQueueMessage{
		MessageID:      v.MessageID,
		FromAgentUUID:  v.FromAgentUUID,
		ToAgentUUID:    v.ToAgentUUID,
		FromAgentID:    v.FromAgentID,
		ToAgentID:      v.ToAgentID,
		FromAgentURI:   v.FromAgentURI,
		ToAgentURI:     v.ToAgentURI,
		SenderOrgID:    v.SenderOrgID,
		ReceiverOrgID:  v.ReceiverOrgID,
		ReceiverPeerID: v.ReceiverPeerID,
		ContentType:    v.ContentType,
		Payload:        v.Payload,
		ClientMsgID:    v.ClientMsgID,
		CreatedAt:      v.CreatedAt,
	}
}

func persistMessageRecord(v model.MessageRecord) s3PersistMessageRecord {
	return s3PersistMessageRecord{
		Message:           persistQueueMessage(v.Message),
		Status:            v.Status,
		AcceptedAt:        v.AcceptedAt,
		UpdatedAt:         v.UpdatedAt,
		FirstReceivedAt:   v.FirstReceivedAt,
		LastLeasedAt:      v.LastLeasedAt,
		LeaseExpiresAt:    v.LeaseExpiresAt,
		AckedAt:           v.AckedAt,
		LastDeliveryID:    v.LastDeliveryID,
		DeliveryAttempts:  v.DeliveryAttempts,
		RequeueCount:      v.RequeueCount,
		IdempotentReplays: v.IdempotentReplays,
		LastFailureReason: v.LastFailureReason,
		LastFailureAt:     v.LastFailureAt,
	}
}

func (v s3PersistMessageRecord) toModel() model.MessageRecord {
	return model.MessageRecord{
		Message:           v.Message.toModel(),
		Status:            v.Status,
		AcceptedAt:        v.AcceptedAt,
		UpdatedAt:         v.UpdatedAt,
		FirstReceivedAt:   v.FirstReceivedAt,
		LastLeasedAt:      v.LastLeasedAt,
		LeaseExpiresAt:    v.LeaseExpiresAt,
		AckedAt:           v.AckedAt,
		LastDeliveryID:    v.LastDeliveryID,
		DeliveryAttempts:  v.DeliveryAttempts,
		RequeueCount:      v.RequeueCount,
		IdempotentReplays: v.IdempotentReplays,
		LastFailureReason: v.LastFailureReason,
		LastFailureAt:     v.LastFailureAt,
	}
}

func persistMessageDelivery(v model.MessageDelivery) s3PersistMessageDelivery {
	return s3PersistMessageDelivery{
		DeliveryID:     v.DeliveryID,
		MessageID:      v.MessageID,
		AgentUUID:      v.AgentUUID,
		Attempt:        v.Attempt,
		LeasedAt:       v.LeasedAt,
		LeaseExpiresAt: v.LeaseExpiresAt,
	}
}

func (v s3PersistMessageDelivery) toModel() model.MessageDelivery {
	return model.MessageDelivery{
		DeliveryID:     v.DeliveryID,
		MessageID:      v.MessageID,
		AgentUUID:      v.AgentUUID,
		Attempt:        v.Attempt,
		LeasedAt:       v.LeasedAt,
		LeaseExpiresAt: v.LeaseExpiresAt,
	}
}

func (v s3PersistQueueMessage) toModel() model.Message {
	return model.Message{
		MessageID:      v.MessageID,
		FromAgentUUID:  v.FromAgentUUID,
		ToAgentUUID:    v.ToAgentUUID,
		FromAgentID:    v.FromAgentID,
		ToAgentID:      v.ToAgentID,
		FromAgentURI:   v.FromAgentURI,
		ToAgentURI:     v.ToAgentURI,
		SenderOrgID:    v.SenderOrgID,
		ReceiverOrgID:  v.ReceiverOrgID,
		ReceiverPeerID: v.ReceiverPeerID,
		ContentType:    v.ContentType,
		Payload:        v.Payload,
		ClientMsgID:    v.ClientMsgID,
		CreatedAt:      v.CreatedAt,
	}
}

func persistAgent(v model.Agent) s3PersistAgent {
	return s3PersistAgent{
		AgentUUID:         v.AgentUUID,
		AgentID:           v.AgentID,
		Handle:            v.Handle,
		HandleFinalizedAt: v.HandleFinalizedAt,
		OrgID:             v.OrgID,
		OwnerHumanID:      v.OwnerHumanID,
		TokenHash:         v.TokenHash,
		Status:            v.Status,
		Metadata:          copyMetadata(v.Metadata),
		CreatedBy:         v.CreatedBy,
		CreatedAt:         v.CreatedAt,
		RevokedAt:         v.RevokedAt,
	}
}

func (v s3PersistAgent) toModel() model.Agent {
	return model.Agent{
		AgentUUID:         v.AgentUUID,
		AgentID:           v.AgentID,
		Handle:            v.Handle,
		HandleFinalizedAt: v.HandleFinalizedAt,
		OrgID:             v.OrgID,
		OwnerHumanID:      v.OwnerHumanID,
		TokenHash:         v.TokenHash,
		Status:            v.Status,
		Metadata:          copyMetadata(v.Metadata),
		CreatedBy:         v.CreatedBy,
		CreatedAt:         v.CreatedAt,
		RevokedAt:         v.RevokedAt,
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

func queueMessageObjectName(message model.Message) string {
	id := strings.TrimSpace(message.MessageID)
	if id == "" {
		id = hashKey(fmt.Sprintf(
			"%s\x00%s\x00%s\x00%s",
			message.FromAgentUUID,
			message.ToAgentUUID,
			message.ContentType,
			message.CreatedAt.UTC().Format(time.RFC3339Nano),
		))
	}
	return escapeKeySegment(message.CreatedAt.UTC().Format(time.RFC3339Nano)+"_"+id) + ".json"
}
