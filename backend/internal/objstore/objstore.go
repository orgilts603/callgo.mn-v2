// Package objstore implements domain.ObjectStore for call recordings and
// exports: an S3-compatible driver (AWS S3, MinIO, Cloudflare R2) built on
// minio-go, and a local-disk driver for development whose signed URLs point
// back at the API (GET /api/recordings/file/<key>?exp=&sig=).
package objstore

import (
	"fmt"
	"path"
	"strings"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// cleanKey validates an object key: relative, slash separated, no "..",
// no backslashes or NUL bytes. It returns the cleaned key.
func cleanKey(key string) (string, error) {
	k := strings.TrimSpace(key)
	if k == "" {
		return "", fmt.Errorf("%w: objstore: empty key", domain.ErrInvalid)
	}
	if strings.ContainsAny(k, "\\\x00") {
		return "", fmt.Errorf("%w: objstore: invalid key %q", domain.ErrInvalid, key)
	}
	if strings.HasPrefix(k, "/") {
		return "", fmt.Errorf("%w: objstore: key %q must be relative", domain.ErrInvalid, key)
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: objstore: key %q escapes the store", domain.ErrInvalid, key)
		}
	}
	c := path.Clean(k)
	if c == "." || c == "" {
		return "", fmt.Errorf("%w: objstore: invalid key %q", domain.ErrInvalid, key)
	}
	return c, nil
}

// contentTypeFor returns the MIME type used when serving/uploading key.
func contentTypeFor(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".mp4", ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	return "application/octet-stream"
}
