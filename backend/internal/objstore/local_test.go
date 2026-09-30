package objstore

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestLocalRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := NewLocal(dir, LocalSigner(testSecret, ""))
	require.NoError(t, err)

	key := "recordings/org/2026/09/call.ogg"
	require.NoError(t, st.Put(ctx, key, "audio/ogg", []byte("OggS-data")))

	size, ok, err := st.Stat(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.EqualValues(t, 9, size)

	b, err := os.ReadFile(filepath.Join(dir, "recordings", "org", "2026", "09", "call.ogg"))
	require.NoError(t, err)
	require.Equal(t, "OggS-data", string(b))

	// Overwrite is atomic and leaves no temp files behind.
	require.NoError(t, st.Put(ctx, key, "", []byte("v2")))
	entries, err := os.ReadDir(filepath.Join(dir, "recordings", "org", "2026", "09"))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.NoError(t, st.Delete(ctx, key))
	_, ok, err = st.Stat(ctx, key)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, st.Delete(ctx, key), "deleting a missing object is not an error")
}

func TestLocalRejectsBadKeys(t *testing.T) {
	st, err := NewLocal(t.TempDir(), nil)
	require.NoError(t, err)
	for _, k := range []string{"", "/etc/passwd", "../x", "a/../../x", `a\b`, "."} {
		err := st.Put(context.Background(), k, "", []byte("x"))
		require.Error(t, err, k)
		require.True(t, errors.Is(err, domain.ErrInvalid), k)
	}
	_, err = st.SignedURL(context.Background(), "a.ogg", time.Minute)
	require.Error(t, err, "no signer configured")
}

func TestLocalSignedURLAndVerify(t *testing.T) {
	st, err := NewLocal(t.TempDir(), LocalSigner(testSecret, ""))
	require.NoError(t, err)
	now := time.Unix(1_700_000_000, 0)
	st.now = func() time.Time { return now }

	raw, err := st.SignedURL(context.Background(), "recordings/o/2026/09/c.ogg", 10*time.Minute)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(raw, LocalFilePrefix+"recordings/o/2026/09/c.ogg?"), raw)

	u, err := url.Parse(raw)
	require.NoError(t, err)
	key := strings.TrimPrefix(u.Path, LocalFilePrefix)
	exp, sig := u.Query().Get("exp"), u.Query().Get("sig")
	require.Equal(t, "1700000600", exp)

	require.True(t, verifyLocalSignatureAt(testSecret, key, exp, sig, now))
	require.True(t, verifyLocalSignatureAt(testSecret, key, exp, strings.ToUpper(sig), now))
	require.False(t, verifyLocalSignatureAt(testSecret, key, exp, sig, now.Add(11*time.Minute)), "expired")
	require.False(t, verifyLocalSignatureAt(testSecret, key+"x", exp, sig, now), "other key")
	require.False(t, verifyLocalSignatureAt(testSecret, key, "1700000601", sig, now), "tampered exp")
	require.False(t, verifyLocalSignatureAt([]byte("other-secret"), key, exp, sig, now), "other secret")
	require.False(t, verifyLocalSignatureAt(testSecret, key, "abc", sig, now), "bad exp")
	require.False(t, verifyLocalSignatureAt(nil, key, exp, sig, now), "no secret")
	require.False(t, VerifyLocalSignature(testSecret, key, exp, sig), "real clock: long expired")

	fresh := LocalSigner(testSecret, "/files")("k.ogg", time.Now().Add(time.Minute))
	fu, err := url.Parse(fresh)
	require.NoError(t, err)
	require.Equal(t, "/files/k.ogg", fu.Path)
	require.True(t, VerifyLocalSignature(testSecret, "k.ogg", fu.Query().Get("exp"), fu.Query().Get("sig")))
}

func TestServeLocal(t *testing.T) {
	dir := t.TempDir()
	st, err := NewLocal(dir, nil)
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(), "a/b.ogg", "", []byte("0123456789")))
	h := http.StripPrefix("/files", ServeLocal(dir))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/files/a/b.ogg", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "audio/ogg", rec.Header().Get("Content-Type"))
	require.Equal(t, "0123456789", rec.Body.String())

	req := httptest.NewRequest(http.MethodGet, "/files/a/b.ogg", nil)
	req.Header.Set("Range", "bytes=2-4")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	require.Equal(t, "234", rec.Body.String())

	for _, p := range []string{"/files/missing.ogg", "/files/a", "/files/../etc/passwd"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, p)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/files/a/b.ogg", nil))
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
