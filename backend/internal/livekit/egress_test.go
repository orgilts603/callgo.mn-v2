package livekit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	lkproto "github.com/livekit/protocol/livekit"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/twitchtv/twirp"
	"google.golang.org/protobuf/proto"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestBuildRecordingRequestS3(t *testing.T) {
	s3 := &S3Target{Endpoint: "http://minio:9000", Region: "us-east-1", Bucket: "callgo-recordings",
		AccessKey: "ak", Secret: "sk", ForcePathStyle: true}
	req, err := buildRecordingRequest("call-123", "recordings/org/2026/09/c.ogg", s3)
	require.NoError(t, err)
	require.Equal(t, "call-123", req.GetRoomName())
	require.True(t, req.GetAudioOnly())
	require.Len(t, req.GetFileOutputs(), 1)
	out := req.GetFileOutputs()[0]
	require.Equal(t, lkproto.EncodedFileType_OGG, out.GetFileType())
	require.Equal(t, "recordings/org/2026/09/c.ogg", out.GetFilepath())
	require.True(t, out.GetDisableManifest())
	up := out.GetS3()
	require.NotNil(t, up)
	require.Equal(t, "http://minio:9000", up.GetEndpoint())
	require.Equal(t, "callgo-recordings", up.GetBucket())
	require.Equal(t, "ak", up.GetAccessKey())
	require.Equal(t, "sk", up.GetSecret())
	require.Equal(t, "us-east-1", up.GetRegion())
	require.True(t, up.GetForcePathStyle())
}

func TestBuildRecordingRequestLocalAndErrors(t *testing.T) {
	req, err := buildRecordingRequest("call-1", "recordings/o/c.ogg", nil)
	require.NoError(t, err)
	out := req.GetFileOutputs()[0]
	require.Nil(t, out.GetS3())
	require.Equal(t, "/out/recordings/recordings/o/c.ogg", out.GetFilepath())

	for _, tc := range []struct {
		room, key string
		s3        *S3Target
	}{
		{"", "a.ogg", nil},
		{"call-1", "", nil},
		{"call-1", "/abs.ogg", nil},
		{"call-1", "../x.ogg", nil},
		{"call-1", "a.ogg", &S3Target{}},
	} {
		_, err := buildRecordingRequest(tc.room, tc.key, tc.s3)
		require.ErrorIs(t, err, domain.ErrInvalid, "%+v", tc)
	}
}

type fakeEgress struct {
	mu      sync.Mutex
	started []*lkproto.RoomCompositeEgressRequest
	stopped []string
	stopErr error
}

func (f *fakeEgress) StartRoomCompositeEgress(_ context.Context, req *lkproto.RoomCompositeEgressRequest) (*lkproto.EgressInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, proto.CloneOf(req))
	return &lkproto.EgressInfo{EgressId: "EG_1", RoomName: req.GetRoomName(), Status: lkproto.EgressStatus_EGRESS_STARTING}, nil
}

func (f *fakeEgress) StopEgress(_ context.Context, req *lkproto.StopEgressRequest) (*lkproto.EgressInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, req.GetEgressId())
	if f.stopErr != nil {
		return nil, f.stopErr
	}
	return &lkproto.EgressInfo{EgressId: req.GetEgressId(), Status: lkproto.EgressStatus_EGRESS_ENDING}, nil
}

func TestStartStopRecordingWithFake(t *testing.T) {
	f := &fakeEgress{}
	id, err := startRecording(context.Background(), f, "call-1", "k.ogg", nil)
	require.NoError(t, err)
	require.Equal(t, "EG_1", id)
	require.Len(t, f.started, 1)

	require.NoError(t, stopRecording(context.Background(), f, "EG_1"))
	f.stopErr = twirp.NewError(twirp.NotFound, "egress not found")
	require.NoError(t, stopRecording(context.Background(), f, "EG_1"))
	f.stopErr = twirp.NewError(twirp.FailedPrecondition, "egress with status EGRESS_COMPLETE cannot be stopped")
	require.NoError(t, stopRecording(context.Background(), f, "EG_1"))
	f.stopErr = twirp.NewError(twirp.Unavailable, "down")
	require.Error(t, stopRecording(context.Background(), f, "EG_1"))
	require.ErrorIs(t, stopRecording(context.Background(), f, ""), domain.ErrInvalid)
}

// wireEgress serves the Egress twirp API for the over-the-wire test. Only the
// methods the recorder calls are implemented.
type wireEgress struct {
	lkproto.Egress
	f *fakeEgress
}

func (w wireEgress) StartRoomCompositeEgress(ctx context.Context, req *lkproto.RoomCompositeEgressRequest) (*lkproto.EgressInfo, error) {
	return w.f.StartRoomCompositeEgress(ctx, req)
}

func (w wireEgress) StopEgress(ctx context.Context, req *lkproto.StopEgressRequest) (*lkproto.EgressInfo, error) {
	return w.f.StopEgress(ctx, req)
}

func TestRecordingOverTheWire(t *testing.T) {
	f := &fakeEgress{}
	srv := lkproto.NewEgressServer(wireEgress{f: f})
	var sawAuth bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			sawAuth = true
		}
		srv.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)

	c, err := NewClient(Config{URL: ts.URL, APIKey: "key", APISecret: "secret-secret-secret-secret-secret"}, zerolog.Nop())
	require.NoError(t, err)
	id, err := c.StartRecording(context.Background(), "call-9", "recordings/o/2026/09/c.ogg",
		&S3Target{Endpoint: "http://minio:9000", Bucket: "b", AccessKey: "a", Secret: "s", ForcePathStyle: true})
	require.NoError(t, err)
	require.Equal(t, "EG_1", id)
	require.True(t, sawAuth)
	require.Len(t, f.started, 1)
	require.True(t, f.started[0].GetAudioOnly())
	require.Equal(t, "b", f.started[0].GetFileOutputs()[0].GetS3().GetBucket())

	require.NoError(t, c.StopRecording(context.Background(), "EG_1"))
	require.Equal(t, []string{"EG_1"}, f.stopped)
}

func egressEvent(status lkproto.EgressStatus, files ...*lkproto.FileInfo) *lkproto.WebhookEvent {
	return &lkproto.WebhookEvent{
		Event: WebhookEgressEnded,
		EgressInfo: &lkproto.EgressInfo{
			EgressId: "EG_abc", RoomName: "call-1", Status: status, FileResults: files,
		},
	}
}

func TestParseEgressEnded(t *testing.T) {
	ev := egressEvent(lkproto.EgressStatus_EGRESS_COMPLETE, &lkproto.FileInfo{
		Filename: "recordings/o/2026/09/c.ogg", Location: "http://minio:9000/b/recordings/o/2026/09/c.ogg",
		Size: 12345, Duration: 61_600_000_000,
	})
	room, id, key, size, dur, ok := ParseEgressEnded(ev)
	require.True(t, ok)
	require.Equal(t, "call-1", room)
	require.Equal(t, "EG_abc", id)
	require.Equal(t, "recordings/o/2026/09/c.ogg", key)
	require.EqualValues(t, 12345, size)
	require.Equal(t, 62, dur)

	// Local egress: the /out/recordings prefix is stripped.
	ev = egressEvent(lkproto.EgressStatus_EGRESS_LIMIT_REACHED, &lkproto.FileInfo{Filename: "/out/recordings/recordings/o/c.ogg", Size: 1})
	_, _, key, _, _, ok = ParseEgressEnded(ev)
	require.True(t, ok)
	require.Equal(t, "recordings/o/c.ogg", key)

	// Legacy single-file result.
	ev = egressEvent(lkproto.EgressStatus_EGRESS_COMPLETE)
	ev.EgressInfo.Result = &lkproto.EgressInfo_File{File: &lkproto.FileInfo{Filename: "x.ogg"}}
	_, _, key, _, _, ok = ParseEgressEnded(ev)
	require.True(t, ok)
	require.Equal(t, "x.ogg", key)

	// Not ok: other events, failures, no file.
	_, _, _, _, _, ok = ParseEgressEnded(&lkproto.WebhookEvent{Event: WebhookRoomFinished})
	require.False(t, ok)
	_, _, _, _, _, ok = ParseEgressEnded(nil)
	require.False(t, ok)
	failedEv := egressEvent(lkproto.EgressStatus_EGRESS_FAILED, &lkproto.FileInfo{Filename: "x.ogg"})
	failedEv.EgressInfo.Error = "upload failed"
	_, _, _, _, _, ok = ParseEgressEnded(failedEv)
	require.False(t, ok)

	room, id, reason, failed := EgressFailure(failedEv)
	require.True(t, failed)
	require.Equal(t, "call-1", room)
	require.Equal(t, "EG_abc", id)
	require.Equal(t, "upload failed", reason)

	_, _, reason, failed = EgressFailure(egressEvent(lkproto.EgressStatus_EGRESS_ABORTED))
	require.True(t, failed)
	require.Equal(t, "aborted", reason)
	_, _, reason, failed = EgressFailure(egressEvent(lkproto.EgressStatus_EGRESS_COMPLETE))
	require.True(t, failed)
	require.Equal(t, "no file produced", reason)
	_, _, _, failed = EgressFailure(ev)
	require.False(t, failed, "successful egress is not a failure")
}

func TestMockRecording(t *testing.T) {
	m := NewMock(MockOptions{})
	t.Cleanup(m.Close)
	id, err := m.StartRecording(context.Background(), "call-1", "k.ogg", nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id, "EG_mock_"))
	require.NoError(t, m.StopRecording(context.Background(), id))
	_, err = m.StartRecording(context.Background(), "", "k.ogg", nil)
	require.True(t, errors.Is(err, domain.ErrInvalid))
}
