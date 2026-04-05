package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"moltenhub/internal/model"
)

const (
	defaultS3Region = "us-east-1"
	defaultS3Prefix = "moltenhub-queue"
	// Bound a queue operation when callers provide no deadline.
	defaultS3QueueOpTimeout = 8 * time.Second
	// Startup check should fail fast so readiness decisions are not delayed too long.
	defaultS3QueueStartupCheckTimeout = 5 * time.Second
)

type s3QueueStore struct {
	conn      *s3Connection
	prefix    string
	opTimeout time.Duration

	dequeueMu sync.Mutex
}

func NewS3QueueStoreFromEnv() (MessageQueueStore, error) {
	endpoint := strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_ENDPOINT"))
	bucket := strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_BUCKET"))
	region := strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_REGION"))
	prefix := strings.Trim(strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_PREFIX")), "/")
	pathStyleRaw := strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_PATH_STYLE"))
	accessKeyID := strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(os.Getenv("MOLTENHUB_QUEUE_S3_SECRET_ACCESS_KEY"))

	if endpoint == "" {
		return nil, fmt.Errorf("MOLTENHUB_QUEUE_S3_ENDPOINT is required for s3 queue backend")
	}
	if bucket == "" {
		return nil, fmt.Errorf("MOLTENHUB_QUEUE_S3_BUCKET is required for s3 queue backend")
	}
	if region == "" {
		region = defaultS3Region
	}
	if prefix == "" {
		prefix = defaultS3Prefix
	}
	if (accessKeyID == "") != (secretAccessKey == "") {
		return nil, fmt.Errorf("MOLTENHUB_QUEUE_S3_ACCESS_KEY_ID and MOLTENHUB_QUEUE_S3_SECRET_ACCESS_KEY must be set together")
	}
	pathStyle := true
	if pathStyleRaw != "" {
		pathStyle = strings.EqualFold(pathStyleRaw, "true")
	}
	if !pathStyle {
		return nil, fmt.Errorf("MOLTENHUB_QUEUE_S3_PATH_STYLE=false is not supported in this build")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("MOLTENHUB_QUEUE_S3_ENDPOINT must include http:// or https:// scheme")
	}
	conn, err := newS3Connection(s3ConnectionConfig{
		HTTPClient:      newS3HTTPClient(10 * time.Second),
		Endpoint:        endpoint,
		Bucket:          bucket,
		Region:          region,
		PathStyle:       pathStyle,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	})
	if err != nil {
		return nil, err
	}

	return &s3QueueStore{
		conn:      conn,
		prefix:    prefix,
		opTimeout: defaultS3QueueOpTimeout,
	}, nil
}

func (s *s3QueueStore) StartupCheck(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultS3QueueStartupCheckTimeout)
		defer cancel()
	}
	if _, err := s.conn.ListObjects(ctx, s.prefix+"/queues/", 1, "", false); err != nil {
		return fmt.Errorf("queue startup check request: %w", err)
	}

	probeKey := fmt.Sprintf("%s/startup-check/%019d.json", s.prefix, time.Now().UTC().UnixNano())
	if err := s.putObject(ctx, probeKey, []byte(`{"check":"queue_startup"}`)); err != nil {
		return fmt.Errorf("queue startup check write failed: %w", err)
	}
	if err := s.deleteObject(ctx, probeKey); err != nil {
		return fmt.Errorf("queue startup check cleanup failed: %w", err)
	}
	return nil
}

func (s *s3QueueStore) Enqueue(ctx context.Context, message model.Message) error {
	ctx, cancel := s.operationContext(ctx)
	defer cancel()

	if message.ToAgentUUID == "" {
		return ErrAgentNotFound
	}
	key := s.queueObjectKey(message.ToAgentUUID, message.CreatedAt, message.MessageID)
	body, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	return s.putObject(ctx, key, body)
}

func (s *s3QueueStore) Dequeue(ctx context.Context, agentUUID string) (model.Message, bool, error) {
	ctx, cancel := s.operationContext(ctx)
	defer cancel()

	if agentUUID == "" {
		return model.Message{}, false, nil
	}

	s.dequeueMu.Lock()
	defer s.dequeueMu.Unlock()

	key, ok, err := s.listOldestKey(ctx, agentUUID)
	if err != nil {
		return model.Message{}, false, err
	}
	if !ok {
		return model.Message{}, false, nil
	}

	msg, err := s.readMessage(ctx, key)
	if err != nil {
		return model.Message{}, false, err
	}
	if err := s.deleteObject(ctx, key); err != nil {
		return model.Message{}, false, err
	}
	return msg, true, nil
}

func (s *s3QueueStore) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	timeout := s.opTimeout
	if timeout <= 0 {
		timeout = defaultS3QueueOpTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *s3QueueStore) queueObjectKey(agentUUID string, createdAt time.Time, messageID string) string {
	ts := createdAt.UnixNano()
	if ts <= 0 {
		ts = time.Now().UnixNano()
	}
	return fmt.Sprintf("%s/queues/%s/%019d_%s.json", s.prefix, agentUUID, ts, messageID)
}

func (s *s3QueueStore) queuePrefix(agentUUID string) string {
	return fmt.Sprintf("%s/queues/%s/", s.prefix, agentUUID)
}

func (s *s3QueueStore) listOldestKey(ctx context.Context, agentUUID string) (string, bool, error) {
	listed, err := s.conn.ListObjects(ctx, s.queuePrefix(agentUUID), 1, "", false)
	if err != nil {
		return "", false, err
	}
	if len(listed.Keys) == 0 || strings.TrimSpace(listed.Keys[0]) == "" {
		return "", false, nil
	}
	return strings.TrimSpace(listed.Keys[0]), true, nil
}

func (s *s3QueueStore) readMessage(ctx context.Context, key string) (model.Message, error) {
	body, found, err := s.conn.GetObject(ctx, key, 4*1024*1024, false)
	if err != nil {
		return model.Message{}, err
	}
	if !found {
		return model.Message{}, fmt.Errorf("get object status %d: not found", 404)
	}
	var msg model.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return model.Message{}, fmt.Errorf("decode message: %w", err)
	}
	return msg, nil
}

func (s *s3QueueStore) deleteObject(ctx context.Context, key string) error {
	return s.conn.DeleteObject(ctx, key, false)
}

func (s *s3QueueStore) putObject(ctx context.Context, key string, body []byte) error {
	return s.conn.PutObject(ctx, key, body, "application/json")
}
