package objstore

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// DefaultRegion is used when S3Config.Region is empty (MinIO's default).
const DefaultRegion = "us-east-1"

// S3Config configures the S3-compatible driver.
type S3Config struct {
	// Endpoint is host[:port] (e.g. "minio:9000", "s3.amazonaws.com"). A
	// scheme ("https://…") is accepted and then decides UseSSL.
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
	// PublicBaseURL is the scheme://host[:port] browsers use to reach the
	// store when it differs from Endpoint (e.g. backend talks to
	// "minio:9000" inside Docker, browsers to "https://s3.example.mn").
	// Presigned URLs are signed for this host. Empty = Endpoint.
	PublicBaseURL string
}

// endpoint returns host[:port] and whether TLS is used.
func (c S3Config) endpoint() (string, bool, error) {
	return splitEndpoint(c.Endpoint, c.UseSSL)
}

// EgressEndpoint is the endpoint URL (with scheme) LiveKit egress uploads to.
func (c S3Config) EgressEndpoint() string {
	host, secure, err := c.endpoint()
	if err != nil {
		return c.Endpoint
	}
	if secure {
		return "https://" + host
	}
	return "http://" + host
}

func splitEndpoint(raw string, useSSL bool) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, fmt.Errorf("%w: objstore: S3 endpoint is empty", domain.ErrInvalid)
	}
	if !strings.Contains(raw, "://") {
		return strings.TrimRight(raw, "/"), useSSL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false, fmt.Errorf("%w: objstore: invalid S3 endpoint %q", domain.ErrInvalid, raw)
	}
	if u.Path != "" && u.Path != "/" {
		return "", false, fmt.Errorf("%w: objstore: S3 endpoint %q must not have a path", domain.ErrInvalid, raw)
	}
	switch u.Scheme {
	case "http":
		return u.Host, false, nil
	case "https":
		return u.Host, true, nil
	}
	return "", false, fmt.Errorf("%w: objstore: S3 endpoint %q: scheme must be http or https", domain.ErrInvalid, raw)
}

// S3 stores objects in an S3-compatible bucket.
type S3 struct {
	cli     *minio.Client
	presign *minio.Client // signs for PublicBaseURL; == cli when not set
	bucket  string
	region  string
}

var _ domain.ObjectStore = (*S3)(nil)

// NewS3 builds the driver. It does not contact the server; call EnsureBucket
// at start-up.
func NewS3(cfg S3Config) (*S3, error) {
	return newS3(cfg, nil)
}

func newS3(cfg S3Config, transport http.RoundTripper) (*S3, error) {
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("%w: objstore: S3 bucket is empty", domain.ErrInvalid)
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("%w: objstore: S3 access key and secret key are required", domain.ErrInvalid)
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = DefaultRegion
	}
	host, secure, err := cfg.endpoint()
	if err != nil {
		return nil, err
	}
	opts := func(secure bool) *minio.Options {
		return &minio.Options{
			Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
			Secure:    secure,
			Region:    region, // set explicitly: no bucket-location round trip
			Transport: transport,
		}
	}
	cli, err := minio.New(host, opts(secure))
	if err != nil {
		return nil, fmt.Errorf("objstore: S3 client: %w", err)
	}
	s := &S3{cli: cli, presign: cli, bucket: cfg.Bucket, region: region}
	if strings.TrimSpace(cfg.PublicBaseURL) != "" {
		phost, psecure, err := splitEndpoint(cfg.PublicBaseURL, false)
		if err != nil {
			return nil, fmt.Errorf("objstore: public base URL: %w", err)
		}
		if !strings.Contains(cfg.PublicBaseURL, "://") {
			psecure = secure
		}
		pc, err := minio.New(phost, opts(psecure))
		if err != nil {
			return nil, fmt.Errorf("objstore: S3 presign client: %w", err)
		}
		s.presign = pc
	}
	return s, nil
}

// Bucket returns the configured bucket name.
func (s *S3) Bucket() string { return s.bucket }

// EnsureBucket creates the bucket when it does not exist.
func (s *S3) EnsureBucket(ctx context.Context) error {
	ok, err := s.cli.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("objstore: check bucket %s: %w", s.bucket, err)
	}
	if ok {
		return nil
	}
	if err := s.cli.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region}); err != nil {
		code := minio.ToErrorResponse(err).Code
		if code == "BucketAlreadyOwnedByYou" || code == "BucketAlreadyExists" {
			return nil
		}
		return fmt.Errorf("objstore: create bucket %s: %w", s.bucket, err)
	}
	return nil
}

// Put uploads body to key.
func (s *S3) Put(ctx context.Context, key, contentType string, body []byte) error {
	k, err := cleanKey(key)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = contentTypeFor(k)
	}
	_, err = s.cli.PutObject(ctx, s.bucket, k, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("objstore: put %s: %w", k, err)
	}
	return nil
}

// SignedURL returns a presigned GET URL valid for ttl (1s..7d).
func (s *S3) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	switch {
	case ttl <= 0:
		ttl = 10 * time.Minute
	case ttl < time.Second:
		ttl = time.Second
	case ttl > 7*24*time.Hour:
		ttl = 7 * 24 * time.Hour
	}
	u, err := s.presign.PresignedGetObject(ctx, s.bucket, k, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("objstore: presign %s: %w", k, err)
	}
	return u.String(), nil
}

// Delete removes key; a missing object is not an error.
func (s *S3) Delete(ctx context.Context, key string) error {
	k, err := cleanKey(key)
	if err != nil {
		return err
	}
	if err := s.cli.RemoveObject(ctx, s.bucket, k, minio.RemoveObjectOptions{}); err != nil {
		if isS3NotFound(err) {
			return nil
		}
		return fmt.Errorf("objstore: delete %s: %w", k, err)
	}
	return nil
}

// Stat reports the size of key and whether it exists.
func (s *S3) Stat(ctx context.Context, key string) (int64, bool, error) {
	k, err := cleanKey(key)
	if err != nil {
		return 0, false, err
	}
	info, err := s.cli.StatObject(ctx, s.bucket, k, minio.StatObjectOptions{})
	if err != nil {
		if isS3NotFound(err) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("objstore: stat %s: %w", k, err)
	}
	return info.Size, true, nil
}

func isS3NotFound(err error) bool {
	r := minio.ToErrorResponse(err)
	if r.Code == "NoSuchKey" || r.Code == "NotFound" {
		return true
	}
	return r.StatusCode == http.StatusNotFound && r.Code != "NoSuchBucket"
}
