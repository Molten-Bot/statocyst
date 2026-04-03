package store

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
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
	httpClient *http.Client
	endpoint   string
	bucket     string
	region     string
	prefix     string
	pathStyle  bool
	signer     *s3Signer
	opTimeout  time.Duration

	dequeueMu sync.Mutex
}

type listBucketResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
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

	return &s3QueueStore{
		httpClient: newS3HTTPClient(10 * time.Second),
		endpoint:   strings.TrimSuffix(endpoint, "/"),
		bucket:     bucket,
		region:     region,
		prefix:     prefix,
		pathStyle:  pathStyle,
		signer:     newS3Signer(accessKeyID, secretAccessKey, region),
		opTimeout:  defaultS3QueueOpTimeout,
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
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("max-keys", "1")
	query.Set("prefix", s.prefix+"/queues/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL("", query), nil)
	if err != nil {
		return fmt.Errorf("build queue startup check request: %w", err)
	}
	if err := s.signRequest(req, nil); err != nil {
		return err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("queue startup check request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("queue startup check status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
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
	query := url.Values{}
	query.Set("list-type", "2")
	query.Set("max-keys", "1")
	query.Set("prefix", s.queuePrefix(agentUUID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL("", query), nil)
	if err != nil {
		return "", false, fmt.Errorf("build list request: %w", err)
	}
	if err := s.signRequest(req, nil); err != nil {
		return "", false, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("list objects: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", false, fmt.Errorf("list objects status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var parsed listBucketResult
	if err := xml.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", false, fmt.Errorf("decode list result: %w", err)
	}
	if len(parsed.Contents) == 0 || strings.TrimSpace(parsed.Contents[0].Key) == "" {
		return "", false, nil
	}
	return strings.TrimSpace(parsed.Contents[0].Key), true, nil
}

func (s *s3QueueStore) readMessage(ctx context.Context, key string) (model.Message, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key, nil), nil)
	if err != nil {
		return model.Message{}, fmt.Errorf("build get request: %w", err)
	}
	if err := s.signRequest(req, nil); err != nil {
		return model.Message{}, err
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return model.Message{}, fmt.Errorf("get object: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return model.Message{}, fmt.Errorf("get object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var msg model.Message
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return model.Message{}, fmt.Errorf("decode message: %w", err)
	}
	return msg, nil
}

func (s *s3QueueStore) deleteObject(ctx context.Context, key string) error {
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
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delete object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func (s *s3QueueStore) putObject(ctx context.Context, key string, body []byte) error {
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

func (s *s3QueueStore) objectURL(key string, query url.Values) string {
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

func (s *s3QueueStore) signRequest(req *http.Request, payload []byte) error {
	if s.signer == nil {
		return nil
	}
	if err := s.signer.Sign(req, payload, time.Now().UTC()); err != nil {
		return fmt.Errorf("sign request: %w", err)
	}
	return nil
}

func escapeS3Path(key string) string {
	parts := strings.Split(strings.Trim(key, "/"), "/")
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		escaped = append(escaped, url.PathEscape(p))
	}
	return strings.Join(escaped, "/")
}

func isS3WriteStatus(code int) bool {
	switch code {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		return true
	default:
		return false
	}
}
