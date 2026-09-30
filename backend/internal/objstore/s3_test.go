package objstore

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeS3 is a tiny path-style S3 server: enough for bucket create/exists and
// object put/head/delete. It does not verify signatures.
type fakeS3 struct {
	mu      sync.Mutex
	buckets map[string]bool
	objects map[string][]byte
	types   map[string]string
}

func newFakeS3() *fakeS3 {
	return &fakeS3{buckets: map[string]bool{}, objects: map[string][]byte{}, types: map[string]string{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(p, "/")
	notFound := func(code string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>`+code+`</Code><Message>not found</Message></Error>`)
		}
	}
	if key == "" {
		switch r.Method {
		case http.MethodHead:
			if !f.buckets[bucket] {
				notFound("NoSuchBucket")
				return
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			f.buckets[bucket] = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotImplemented)
		}
		return
	}
	if !f.buckets[bucket] {
		notFound("NoSuchBucket")
		return
	}
	id := bucket + "/" + key
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Amz-Content-Sha256") == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" {
			body = decodeAWSChunked(body)
		}
		f.objects[id] = body
		f.types[id] = r.Header.Get("Content-Type")
		sum := md5.Sum(body)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead, http.MethodGet:
		b, ok := f.objects[id]
		if !ok {
			notFound("NoSuchKey")
			return
		}
		sum := md5.Sum(b)
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Header().Set("Content-Type", f.types[id])
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(b)
		}
	case http.MethodDelete:
		delete(f.objects, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

// decodeAWSChunked strips aws-chunked framing ("<hex>;chunk-signature=…\r\n<data>\r\n").
func decodeAWSChunked(b []byte) []byte {
	var out []byte
	s := string(b)
	for s != "" {
		line, rest, ok := strings.Cut(s, "\r\n")
		if !ok {
			break
		}
		sizeHex, _, _ := strings.Cut(line, ";")
		n, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil || n == 0 || int(n) > len(rest) {
			break
		}
		out = append(out, rest[:n]...)
		s = strings.TrimPrefix(rest[n:], "\r\n")
	}
	return out
}

func TestS3AgainstFake(t *testing.T) {
	fake := newFakeS3()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)

	st, err := NewS3(S3Config{
		Endpoint: srv.URL, Bucket: "callgo-recordings", AccessKey: "ak", SecretKey: "sk-secret",
		PublicBaseURL: "https://files.example.mn",
	})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.EnsureBucket(ctx))
	require.True(t, fake.buckets["callgo-recordings"])
	require.NoError(t, st.EnsureBucket(ctx), "idempotent")

	key := "recordings/org/2026/09/call.ogg"
	require.NoError(t, st.Put(ctx, key, "", []byte("OggS-bytes")))
	require.Equal(t, "OggS-bytes", string(fake.objects["callgo-recordings/"+key]))
	require.Equal(t, "audio/ogg", fake.types["callgo-recordings/"+key])

	size, ok, err := st.Stat(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 10, size)

	signed, err := st.SignedURL(ctx, key, 10*time.Minute)
	require.NoError(t, err)
	su, err := url.Parse(signed)
	require.NoError(t, err)
	require.Equal(t, "https", su.Scheme)
	require.Equal(t, "files.example.mn", su.Host, "signed for the public host")
	require.Equal(t, "/callgo-recordings/"+key, su.Path)
	require.Equal(t, "600", su.Query().Get("X-Amz-Expires"))
	require.NotEmpty(t, su.Query().Get("X-Amz-Signature"))

	require.NoError(t, st.Delete(ctx, key))
	_, ok, err = st.Stat(ctx, key)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, st.Delete(ctx, key))

	require.Equal(t, "http://"+u.Host, S3Config{Endpoint: srv.URL}.EgressEndpoint())
	require.Equal(t, "https://minio:9000", S3Config{Endpoint: "minio:9000", UseSSL: true}.EgressEndpoint())
}

func TestS3ConfigValidation(t *testing.T) {
	_, err := NewS3(S3Config{Endpoint: "minio:9000", AccessKey: "a", SecretKey: "b"})
	require.Error(t, err, "bucket required")
	_, err = NewS3(S3Config{Endpoint: "minio:9000", Bucket: "b"})
	require.Error(t, err, "credentials required")
	_, err = NewS3(S3Config{Endpoint: "ftp://x", Bucket: "b", AccessKey: "a", SecretKey: "b"})
	require.Error(t, err)
	_, err = NewS3(S3Config{Endpoint: "http://x/path", Bucket: "b", AccessKey: "a", SecretKey: "b"})
	require.Error(t, err)
	_, err = NewS3(S3Config{Endpoint: "", Bucket: "b", AccessKey: "a", SecretKey: "b"})
	require.Error(t, err)
}

// TestS3Integration runs against a real MinIO when MINIO_TEST_ENDPOINT is set
// (e.g. MINIO_TEST_ENDPOINT=localhost:9000 MINIO_TEST_ACCESS_KEY=minioadmin
// MINIO_TEST_SECRET_KEY=minioadmin).
func TestS3Integration(t *testing.T) {
	ep := os.Getenv("MINIO_TEST_ENDPOINT")
	if ep == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set")
	}
	st, err := NewS3(S3Config{
		Endpoint: ep, Bucket: "callgo-test-" + strconv.FormatInt(time.Now().UnixNano(), 36),
		AccessKey: os.Getenv("MINIO_TEST_ACCESS_KEY"), SecretKey: os.Getenv("MINIO_TEST_SECRET_KEY"),
	})
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, st.EnsureBucket(ctx))
	require.NoError(t, st.Put(ctx, "a/b.ogg", "", []byte("hello")))
	signed, err := st.SignedURL(ctx, "a/b.ogg", time.Minute)
	require.NoError(t, err)
	res, err := http.Get(signed)
	require.NoError(t, err)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "hello", string(body))
	require.NoError(t, st.Delete(ctx, "a/b.ogg"))
}
