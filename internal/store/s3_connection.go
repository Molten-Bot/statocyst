package store

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultS3ConnectionWorkers    = 64
	defaultS3ConnectionQueueDepth = 512
)

type s3ConnectionConfig struct {
	HTTPClient      *http.Client
	Endpoint        string
	Bucket          string
	Region          string
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	WorkerCount     int
	QueueDepth      int
}

type s3ListObjectsResult struct {
	Keys                  []string
	IsTruncated           bool
	NextContinuationToken string
}

type s3ConnectionListBucketResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

type s3ConnectionJob struct {
	ctx  context.Context
	exec func(context.Context) (any, error)
	done chan s3ConnectionResult
}

type s3ConnectionResult struct {
	value any
	err   error
}

type s3GetObjectResult struct {
	body  []byte
	found bool
}

type s3Connection struct {
	httpClient *http.Client
	endpoint   string
	bucket     string
	pathStyle  bool
	signer     *s3Signer

	jobs   chan s3ConnectionJob
	stopMu sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newS3Connection(cfg s3ConnectionConfig) (*s3Connection, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("s3 endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return nil, fmt.Errorf("s3 endpoint must include http:// or https:// scheme")
	}
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, fmt.Errorf("s3 bucket is required")
	}
	if !cfg.PathStyle {
		return nil, fmt.Errorf("path-style addressing is required in this build")
	}
	if (strings.TrimSpace(cfg.AccessKeyID) == "") != (strings.TrimSpace(cfg.SecretAccessKey) == "") {
		return nil, fmt.Errorf("s3 access key id and secret access key must be set together")
	}

	workers := cfg.WorkerCount
	if workers <= 0 {
		workers = defaultS3ConnectionWorkers
		if cpus := runtime.GOMAXPROCS(0); cpus > 0 {
			adaptive := cpus * 8
			if adaptive > workers {
				workers = adaptive
			}
		}
	}
	queueDepth := cfg.QueueDepth
	if queueDepth <= 0 {
		queueDepth = defaultS3ConnectionQueueDepth
		if workers > 0 && queueDepth < workers*4 {
			queueDepth = workers * 4
		}
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = newS3HTTPClient(10 * time.Second)
	}

	conn := &s3Connection{
		httpClient: httpClient,
		endpoint:   strings.TrimSuffix(endpoint, "/"),
		bucket:     bucket,
		pathStyle:  cfg.PathStyle,
		signer:     newS3Signer(strings.TrimSpace(cfg.AccessKeyID), strings.TrimSpace(cfg.SecretAccessKey), strings.TrimSpace(cfg.Region)),
		jobs:       make(chan s3ConnectionJob, queueDepth),
	}
	for i := 0; i < workers; i++ {
		conn.wg.Add(1)
		go conn.workerLoop()
	}
	return conn, nil
}

func (c *s3Connection) Close() {
	c.stopMu.Lock()
	if c.closed {
		c.stopMu.Unlock()
		return
	}
	c.closed = true
	close(c.jobs)
	c.stopMu.Unlock()
	c.wg.Wait()
}

func (c *s3Connection) ListObjects(ctx context.Context, prefix string, maxKeys int, continuationToken string, allowNotFound bool) (s3ListObjectsResult, error) {
	value, err := c.dispatch(ctx, func(ctx context.Context) (any, error) {
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("prefix", strings.TrimSpace(prefix))
		if continuationToken = strings.TrimSpace(continuationToken); continuationToken != "" {
			query.Set("continuation-token", continuationToken)
		}
		if maxKeys > 0 {
			query.Set("max-keys", strconv.Itoa(maxKeys))
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL("", query), nil)
		if err != nil {
			return s3ListObjectsResult{}, fmt.Errorf("build list request: %w", err)
		}
		if err := c.signRequest(req, nil); err != nil {
			return s3ListObjectsResult{}, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return s3ListObjectsResult{}, fmt.Errorf("list objects: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && allowNotFound {
			return s3ListObjectsResult{}, nil
		}
		if resp.StatusCode != http.StatusOK {
			return s3ListObjectsResult{}, fmt.Errorf("list objects status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var parsed s3ConnectionListBucketResult
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return s3ListObjectsResult{}, fmt.Errorf("decode list result: %w", err)
		}
		keys := make([]string, 0, len(parsed.Contents))
		for _, item := range parsed.Contents {
			key := strings.TrimSpace(item.Key)
			if key == "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return s3ListObjectsResult{
			Keys:                  keys,
			IsTruncated:           parsed.IsTruncated,
			NextContinuationToken: strings.TrimSpace(parsed.NextContinuationToken),
		}, nil
	})
	if err != nil {
		return s3ListObjectsResult{}, err
	}
	result, ok := value.(s3ListObjectsResult)
	if !ok {
		return s3ListObjectsResult{}, fmt.Errorf("internal error: unexpected list objects result type %T", value)
	}
	return result, nil
}

func (c *s3Connection) PutObject(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := c.dispatch(ctx, func(ctx context.Context) (any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.objectURL(key, nil), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("build put request: %w", err)
		}
		if strings.TrimSpace(contentType) != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if err := c.signRequest(req, body); err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("put object: %w", err)
		}
		defer resp.Body.Close()
		if !isS3WriteStatus(resp.StatusCode) {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, fmt.Errorf("put object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		return nil, nil
	})
	return err
}

func (c *s3Connection) GetObject(ctx context.Context, key string, maxBytes int64, allowNotFound bool) ([]byte, bool, error) {
	value, err := c.dispatch(ctx, func(ctx context.Context) (any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.objectURL(key, nil), nil)
		if err != nil {
			return s3GetObjectResult{}, fmt.Errorf("build get request: %w", err)
		}
		if err := c.signRequest(req, nil); err != nil {
			return s3GetObjectResult{}, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return s3GetObjectResult{}, fmt.Errorf("get object: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && allowNotFound {
			return s3GetObjectResult{found: false}, nil
		}
		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return s3GetObjectResult{}, fmt.Errorf("get object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		if maxBytes <= 0 {
			maxBytes = 4 * 1024 * 1024
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
		if err != nil {
			return s3GetObjectResult{}, fmt.Errorf("read object: %w", err)
		}
		return s3GetObjectResult{body: body, found: true}, nil
	})
	if err != nil {
		return nil, false, err
	}
	result, ok := value.(s3GetObjectResult)
	if !ok {
		return nil, false, fmt.Errorf("internal error: unexpected get object result type %T", value)
	}
	return result.body, result.found, nil
}

func (c *s3Connection) DeleteObject(ctx context.Context, key string, allowNotFound bool) error {
	_, err := c.dispatch(ctx, func(ctx context.Context) (any, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.objectURL(key, nil), nil)
		if err != nil {
			return nil, fmt.Errorf("build delete request: %w", err)
		}
		if err := c.signRequest(req, nil); err != nil {
			return nil, err
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("delete object: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound && allowNotFound {
			return nil, nil
		}
		if !isS3WriteStatus(resp.StatusCode) {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return nil, fmt.Errorf("delete object status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
		}
		return nil, nil
	})
	return err
}

func (c *s3Connection) dispatch(ctx context.Context, exec func(context.Context) (any, error)) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	job := s3ConnectionJob{
		ctx:  ctx,
		exec: exec,
		done: make(chan s3ConnectionResult, 1),
	}
	select {
	case c.jobs <- job:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case result := <-job.done:
		return result.value, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *s3Connection) workerLoop() {
	defer c.wg.Done()
	for job := range c.jobs {
		if job.ctx != nil {
			select {
			case <-job.ctx.Done():
				job.done <- s3ConnectionResult{err: job.ctx.Err()}
				continue
			default:
			}
		}
		value, err := job.exec(job.ctx)
		job.done <- s3ConnectionResult{value: value, err: err}
	}
}

func (c *s3Connection) objectURL(key string, query url.Values) string {
	u, _ := url.Parse(c.endpoint)
	if c.pathStyle {
		p := path.Join("/", c.bucket)
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

func (c *s3Connection) signRequest(req *http.Request, payload []byte) error {
	if c.signer == nil {
		return nil
	}
	if err := c.signer.Sign(req, payload, time.Now().UTC()); err != nil {
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
