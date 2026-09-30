package zzcheck

import (
	"github.com/orgilts603/callgo.mn-v2/backend/internal/crm"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/httpapi"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/livekit"
	"github.com/orgilts603/callgo.mn-v2/backend/internal/recording"
)

var _ recording.CallRepo = (*crm.Store)(nil)
var _ recording.OrgRepo = (*crm.Store)(nil)
var _ recording.Egress = (*livekit.Client)(nil)
var _ recording.Egress = (*livekit.Mock)(nil)
var _ httpapi.RecordingService = (*recording.Service)(nil)
