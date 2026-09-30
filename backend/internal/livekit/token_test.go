package livekit

import (
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/stretchr/testify/require"

	"github.com/orgilts603/callgo.mn-v2/backend/internal/domain"
)

func TestOperatorToken(t *testing.T) {
	const key, secret = "devkey", "devsecret-callgo-local-only-change-me-0123456789"
	tok, err := OperatorToken(key, secret, "call-1", "op-42", "Бат", map[string]string{
		"callgo.role": "operator", "callgo.userId": "42",
	}, 0)
	require.NoError(t, err)

	v, err := auth.ParseAPIToken(tok)
	require.NoError(t, err)
	require.Equal(t, key, v.APIKey())
	require.Equal(t, "op-42", v.Identity())
	claims, grants, err := v.Verify(secret)
	require.NoError(t, err)
	require.Equal(t, "Бат", grants.Name)
	require.Equal(t, "operator", grants.Attributes["callgo.role"])
	require.Equal(t, "42", grants.Attributes["callgo.userId"])
	require.NotNil(t, grants.Video)
	require.True(t, grants.Video.RoomJoin)
	require.Equal(t, "call-1", grants.Video.Room)
	require.True(t, grants.Video.GetCanPublish())
	require.True(t, grants.Video.GetCanSubscribe())
	require.WithinDuration(t, time.Now().Add(time.Hour), claims.ExpiresAt.Time, 5*time.Second)

	_, _, err = v.Verify("wrong-secret-wrong-secret-wrong-secret")
	require.Error(t, err)

	_, err = OperatorToken(key, secret, "", "op", "n", nil, time.Minute)
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = OperatorToken("", secret, "r", "op", "n", nil, time.Minute)
	require.ErrorIs(t, err, domain.ErrInvalid)
	_, err = OperatorToken(key, secret, "r", " ", "n", nil, time.Minute)
	require.ErrorIs(t, err, domain.ErrInvalid)
}
