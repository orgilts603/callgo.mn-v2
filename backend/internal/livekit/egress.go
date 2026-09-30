package livekit

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
	lkproto "github.com/livekit/protocol/livekit"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

// LocalRecordingDir is where the egress container writes recordings when no
// S3 target is given (the shared `recordings` volume is mounted at /out).
const LocalRecordingDir = "/out/recordings"

// S3Target is where LiveKit egress uploads a recording. It is sent with every
// request, so egress.yaml needs no storage section.
type S3Target struct {
	// Endpoint with scheme as seen from the egress container, e.g.
	// "http://minio:9000". Empty = AWS S3.
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	Secret    string
	// ForcePathStyle is required by MinIO and most S3-compatible stores.
	ForcePathStyle bool
}

// egressAPI is the subset of *lksdk.EgressClient the recorder uses.
type egressAPI interface {
	StartRoomCompositeEgress(ctx context.Context, req *lkproto.RoomCompositeEgressRequest) (*lkproto.EgressInfo, error)
	StopEgress(ctx context.Context, req *lkproto.StopEgressRequest) (*lkproto.EgressInfo, error)
}

func (c *Client) egressClient() egressAPI {
	return lksdk.NewEgressClient(c.cfg.URL, c.cfg.APIKey, c.cfg.APISecret)
}

// StartRecording starts an audio-only room-composite egress of roomName that
// writes an OGG file to key: uploaded to s3 when given, otherwise written to
// LocalRecordingDir/<key> inside the egress container. It returns the egress
// ID; the result arrives later through the egress_ended webhook (see
// ParseEgressEnded).
func (c *Client) StartRecording(ctx context.Context, roomName, key string, s3 *S3Target) (string, error) {
	return startRecording(ctx, c.egressClient(), roomName, key, s3)
}

// StopRecording stops an egress. An egress that no longer exists or already
// ended is not an error.
func (c *Client) StopRecording(ctx context.Context, egressID string) error {
	return stopRecording(ctx, c.egressClient(), egressID)
}

func startRecording(ctx context.Context, api egressAPI, roomName, key string, s3 *S3Target) (string, error) {
	req, err := buildRecordingRequest(roomName, key, s3)
	if err != nil {
		return "", err
	}
	info, err := api.StartRoomCompositeEgress(ctx, req)
	if err != nil {
		if isNotFound(err) {
			return "", fmt.Errorf("livekit: start recording of %s: %w", roomName, domain.ErrNotFound)
		}
		return "", fmt.Errorf("livekit: start recording of %s: %w", roomName, err)
	}
	id := info.GetEgressId()
	if id == "" {
		return "", fmt.Errorf("livekit: start recording of %s: empty egress id", roomName)
	}
	return id, nil
}

func stopRecording(ctx context.Context, api egressAPI, egressID string) error {
	if strings.TrimSpace(egressID) == "" {
		return fmt.Errorf("%w: missing egress id", domain.ErrInvalid)
	}
	if _, err := api.StopEgress(ctx, &lkproto.StopEgressRequest{EgressId: egressID}); err != nil {
		if isNotFound(err) || isEgressNotActive(err) {
			return nil
		}
		return fmt.Errorf("livekit: stop egress %s: %w", egressID, err)
	}
	return nil
}

// isEgressNotActive matches the error LiveKit returns when stopping an egress
// that already finished ("egress with status EGRESS_COMPLETE cannot be stopped").
func isEgressNotActive(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cannot be stopped") || strings.Contains(msg, "not active") ||
		strings.Contains(msg, "egress_complete") || strings.Contains(msg, "egress_failed") ||
		strings.Contains(msg, "egress_aborted")
}

// buildRecordingRequest is the pure request builder of StartRecording.
func buildRecordingRequest(roomName, key string, s3 *S3Target) (*lkproto.RoomCompositeEgressRequest, error) {
	roomName = strings.TrimSpace(roomName)
	if roomName == "" {
		return nil, fmt.Errorf("%w: recording: missing room name", domain.ErrInvalid)
	}
	k, err := cleanRecordingKey(key)
	if err != nil {
		return nil, err
	}
	out := &lkproto.EncodedFileOutput{FileType: lkproto.EncodedFileType_OGG, DisableManifest: true}
	if s3 != nil {
		if strings.TrimSpace(s3.Bucket) == "" {
			return nil, fmt.Errorf("%w: recording: S3 target without bucket", domain.ErrInvalid)
		}
		out.Filepath = k
		out.Output = &lkproto.EncodedFileOutput_S3{S3: &lkproto.S3Upload{
			AccessKey:      s3.AccessKey,
			Secret:         s3.Secret,
			Region:         s3.Region,
			Endpoint:       s3.Endpoint,
			Bucket:         s3.Bucket,
			ForcePathStyle: s3.ForcePathStyle,
		}}
	} else {
		out.Filepath = path.Join(LocalRecordingDir, k)
	}
	return &lkproto.RoomCompositeEgressRequest{
		RoomName:    roomName,
		AudioOnly:   true,
		FileOutputs: []*lkproto.EncodedFileOutput{out},
	}, nil
}

func cleanRecordingKey(key string) (string, error) {
	k := strings.TrimSpace(key)
	if k == "" || strings.HasPrefix(k, "/") || strings.Contains(k, "\\") {
		return "", fmt.Errorf("%w: recording: invalid object key %q", domain.ErrInvalid, key)
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: recording: invalid object key %q", domain.ErrInvalid, key)
		}
	}
	return path.Clean(k), nil
}

// ParseEgressEnded extracts the recording of a successful egress_ended
// webhook. objectKeyOrPath is the object key as requested (the S3 key, or for
// local egress the path relative to LocalRecordingDir; other absolute paths
// are returned unchanged). ok is false for other events and for egresses that
// failed or produced no file (see EgressFailure).
func ParseEgressEnded(ev *lkproto.WebhookEvent) (roomName, egressID, objectKeyOrPath string, sizeBytes int64, durationSec int, ok bool) {
	if ev == nil || ev.GetEvent() != WebhookEgressEnded || ev.GetEgressInfo() == nil {
		return "", "", "", 0, 0, false
	}
	eg := ev.GetEgressInfo()
	switch eg.GetStatus() {
	case lkproto.EgressStatus_EGRESS_COMPLETE, lkproto.EgressStatus_EGRESS_LIMIT_REACHED:
	default:
		return "", "", "", 0, 0, false
	}
	f := firstFileResult(eg)
	if f == nil || f.GetFilename() == "" && f.GetLocation() == "" {
		return "", "", "", 0, 0, false
	}
	name := f.GetFilename()
	if name == "" {
		name = f.GetLocation()
	}
	return eg.GetRoomName(), eg.GetEgressId(), recordingKeyFromPath(name), f.GetSize(), nanosToSec(f.GetDuration()), true
}

// EgressFailure reports an egress_ended webhook of an egress that failed or
// was aborted, or completed without a file.
func EgressFailure(ev *lkproto.WebhookEvent) (roomName, egressID, reason string, failed bool) {
	if ev == nil || ev.GetEvent() != WebhookEgressEnded || ev.GetEgressInfo() == nil {
		return "", "", "", false
	}
	if _, _, _, _, _, ok := ParseEgressEnded(ev); ok {
		return "", "", "", false
	}
	eg := ev.GetEgressInfo()
	reason = eg.GetError()
	if reason == "" {
		reason = strings.ToLower(strings.TrimPrefix(eg.GetStatus().String(), "EGRESS_"))
		if eg.GetStatus() == lkproto.EgressStatus_EGRESS_COMPLETE {
			reason = "no file produced"
		}
	}
	return eg.GetRoomName(), eg.GetEgressId(), reason, true
}

func firstFileResult(eg *lkproto.EgressInfo) *lkproto.FileInfo {
	for _, f := range eg.GetFileResults() {
		if f != nil && (f.GetFilename() != "" || f.GetLocation() != "") {
			return f
		}
	}
	//nolint:staticcheck // legacy single-file result, still sent by older egress
	return eg.GetFile()
}

// recordingKeyFromPath maps an egress file name back to the object key.
func recordingKeyFromPath(p string) string {
	if rest, ok := strings.CutPrefix(p, LocalRecordingDir+"/"); ok {
		return rest
	}
	return p
}

func nanosToSec(ns int64) int {
	if ns <= 0 {
		return 0
	}
	return int((ns + 500_000_000) / 1_000_000_000)
}

// ---------------------------------------------------------------------------
// Mock
// ---------------------------------------------------------------------------

// StartRecording pretends to start an egress and returns a fake ID. Nothing
// is recorded and no egress_ended webhook will follow.
func (m *Mock) StartRecording(_ context.Context, roomName, key string, s3 *S3Target) (string, error) {
	if _, err := buildRecordingRequest(roomName, key, s3); err != nil {
		return "", err
	}
	return "EG_mock_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12], nil
}

// StopRecording is a no-op for the Mock.
func (m *Mock) StopRecording(_ context.Context, egressID string) error {
	if strings.TrimSpace(egressID) == "" {
		return fmt.Errorf("%w: missing egress id", domain.ErrInvalid)
	}
	return nil
}
