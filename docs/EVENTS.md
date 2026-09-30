# Live events

Envelope (Go `domain.Event`):
```json
{"id":"01J...","type":"transcript.final","orgId":"…","callId":"…","at":"2026-09-30T10:00:00Z","payload":{…}}
```
`id` is a ULID/UUID assigned by the producer (agent or backend). Browsers use it for de-duplication.

| type | payload |
|---|---|
| `call.started` | `{"call": Call}` |
| `call.ringing` | `{"call": Call}` |
| `call.answered` | `{"call": Call}` |
| `call.ended` | `{"call": Call, "endReason": "hangup_customer|hangup_agent|no_answer|busy|failed|max_duration|transferred|voicemail", "summary": "...", "sentiment": "positive|neutral|negative", "intent": "...", "durationSec": 123, "llmModelUsed": "google/gemini-2.5-flash"}` — when sent by the agent, `call` may be omitted; the backend fills it. |
| `call.updated` | `{"call": Call}` (summary/sentiment/recording filled in later) |
| `transcript.partial` | `{"speaker": "customer|agent", "text": "…", "startMs": 0}` (not persisted) |
| `transcript.final` | `{"turn": TranscriptTurn}` — from the agent `turn.id` may be empty; backend assigns id/seq and re-broadcasts with the persisted turn. `rawText` = STT output before lexicon/normalizer. |
| `agent.state` | `{"state": "initializing|listening|thinking|speaking|idle", "llmModel": "…"}` |
| `campaign.progress` | `{"campaign": Campaign, "target": CampaignTarget|null}` |
| `lexicon.updated` | `{"correction": LexiconCorrection, "action": "created|updated|deleted"}` |
| `system` | `{"hello": true, "activeCalls": Call[]}` on connect; `{"message": "..."}` otherwise |
