package objstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// LocalFilePrefix is the API path the local driver's signed URLs point at.
const LocalFilePrefix = "/api/recordings/file/"

// Local stores objects as files under a directory. It is meant for
// development and single-host deployments (LiveKit egress writing to a shared
// volume). SignedURL delegates to signer, normally LocalSigner.
type Local struct {
	dir    string
	signer func(key string, exp time.Time) string
	now    func() time.Time
}

var _ domain.ObjectStore = (*Local)(nil)

// NewLocal returns a local-disk store rooted at dir (created if missing).
// signer builds the URL returned by SignedURL; nil makes SignedURL fail.
func NewLocal(dir string, signer func(key string, exp time.Time) string) (*Local, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("%w: objstore: local dir is empty", domain.ErrInvalid)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("objstore: resolve %s: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("objstore: create %s: %w", abs, err)
	}
	return &Local{dir: abs, signer: signer, now: time.Now}, nil
}

// Dir returns the absolute root directory.
func (l *Local) Dir() string { return l.dir }

// Path returns the file path of key inside the store.
func (l *Local) Path(key string) (string, error) {
	return localPath(l.dir, key)
}

func localPath(dir, key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(k)), nil
}

// Put writes body to key atomically (temp file + rename).
func (l *Local) Put(ctx context.Context, key, _ string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := l.Path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("objstore: mkdir for %s: %w", key, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return fmt.Errorf("objstore: put %s: %w", key, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(body)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("objstore: put %s: %w", key, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("objstore: put %s: %w", key, err)
	}
	if err := os.Rename(tmpName, p); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("objstore: put %s: %w", key, err)
	}
	return nil
}

// SignedURL returns signer(key, now+ttl).
func (l *Local) SignedURL(_ context.Context, key string, ttl time.Duration) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	if l.signer == nil {
		return "", errors.New("objstore: local store has no URL signer")
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return l.signer(k, l.now().Add(ttl)), nil
}

// Delete removes key; a missing object is not an error.
func (l *Local) Delete(_ context.Context, key string) error {
	p, err := l.Path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("objstore: delete %s: %w", key, err)
	}
	return nil
}

// Stat reports the size of key and whether it exists.
func (l *Local) Stat(_ context.Context, key string) (int64, bool, error) {
	p, err := l.Path(key)
	if err != nil {
		return 0, false, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("objstore: stat %s: %w", key, err)
	}
	if fi.IsDir() {
		return 0, false, nil
	}
	return fi.Size(), true, nil
}

// ---------------------------------------------------------------------------
// Signatures
// ---------------------------------------------------------------------------

// SignLocal returns the hex HMAC-SHA256 of key and exp (unix seconds).
func SignLocal(secret []byte, key string, exp int64) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("callgo-objstore-v1\n"))
	m.Write([]byte(key))
	m.Write([]byte{'\n'})
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// LocalSigner returns a signer for NewLocal producing
// "<prefix><escaped key>?exp=<unix>&sig=<hex>". prefix defaults to
// LocalFilePrefix.
func LocalSigner(secret []byte, prefix string) func(key string, exp time.Time) string {
	if prefix == "" {
		prefix = LocalFilePrefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return func(key string, exp time.Time) string {
		e := exp.Unix()
		q := url.Values{}
		q.Set("exp", strconv.FormatInt(e, 10))
		q.Set("sig", SignLocal(secret, key, e))
		return (&url.URL{Path: prefix + key}).EscapedPath() + "?" + q.Encode()
	}
}

// VerifyLocalSignature checks a signature produced by LocalSigner: sig must
// match key and exp (unix seconds, as sent in the URL) and exp must not be in
// the past. The comparison is constant time.
func VerifyLocalSignature(secret []byte, key, exp, sig string) bool {
	return verifyLocalSignatureAt(secret, key, exp, sig, time.Now())
}

func verifyLocalSignatureAt(secret []byte, key, exp, sig string, now time.Time) bool {
	if len(secret) == 0 || key == "" || sig == "" {
		return false
	}
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > e {
		return false
	}
	want := SignLocal(secret, key, e)
	return hmac.Equal([]byte(want), []byte(strings.ToLower(sig)))
}

// ---------------------------------------------------------------------------
// Serving
// ---------------------------------------------------------------------------

// ServeLocal serves the files of a local store. The request path (after any
// http.StripPrefix) is the object key. It performs NO authorisation: mount it
// behind signature verification (see VerifyLocalSignature). Range requests
// are supported so audio players can seek.
func ServeLocal(dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/")
		p, err := localPath(dir, key)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil || fi.IsDir() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentTypeFor(key))
		w.Header().Set("Cache-Control", "private, max-age=600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, filepath.Base(p), fi.ModTime(), f)
	})
}
