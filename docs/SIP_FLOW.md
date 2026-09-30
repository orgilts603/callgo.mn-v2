# CallGo.mn — SIP call flows / Дуудлагын урсгал

How a phone call travels through **Carrier → Asterisk → livekit-sip →
livekit-server → agent → backend → browser**, for inbound and outbound calls,
including webhooks and internal API calls. Configuration details:
[ASTERISK.md](ASTERISK.md), [DEPLOY.md](DEPLOY.md); API contract: [API.md](API.md),
events: [EVENTS.md](EVENTS.md).

The inbound and outbound SIP legs (Asterisk ⇄ livekit-sip ⇄ livekit-server,
webhooks, room naming) were exercised against the real Asterisk 22.10.1,
livekit-sip v1.17 and livekit-server v1.13.7 binaries with a simulated carrier.

## Identifiers / Танигч

| Thing | Value | Set by |
|---|---|---|
| Inbound room | `call_<caller E.164>_<random>` e.g. `call_+97699112233_S4ccFjTJAfSr` | dispatch rule `dispatch_rule_individual{room_prefix:"call"}` |
| Outbound room | `call-<callId uuid>` | backend (`RoomNameForCall`) |
| SIP participant (inbound) | identity `sip_<caller>`, kind `SIP`, attributes `sip.phoneNumber`, `sip.trunkPhoneNumber`, `callgo.direction=inbound`, `callgo.sipNumberId` | livekit-sip + dispatch rule `attributes` |
| SIP participant (outbound) | identity chosen by the backend, attributes `callgo.callId`, `callgo.direction=outbound` | `CreateSIPParticipant` |
| Agent job metadata (inbound) | `{"sipNumberId":"…","direction":"inbound"}` | dispatch rule `room_config.agents[0].metadata` |
| Agent job metadata (outbound) | `{"callId","direction":"outbound","toNumber","fromNumber","sipNumberId","agentProfileId",…}` | backend `CreateRoom` agent dispatch |
| LiveKit SIP objects | `callgo-in-<E.164>`, `callgo-rule-<E.164>`, `callgo-out-<E.164>` | backend `EnsureNumberProvisioned` / `lk-setup.sh` |
| Asterisk channel | `PJSIP/carrier-…` (PSTN leg), `PJSIP/livekit-…` (LiveKit leg) | Asterisk |

---

## 1. Inbound call / Орж ирэх дуудлага

```mermaid
sequenceDiagram
    autonumber
    participant C as Carrier SBC
    participant A as Asterisk
    participant S as livekit-sip
    participant L as livekit-server
    participant G as Agent worker
    participant B as Backend
    participant W as Browser (Live Desk)

    C->>A: INVITE sip:77001234@SIP_EXTERNAL_IP (From: 99112233)
    Note over A: [from-carrier] DID/CLI → E.164<br/>+97677001234 / +97699112233
    A-->>C: 100 Trying
    A->>S: INVITE sip:+97677001234@172.28.0.11:5060<br/>X-CallGo-DID, PAI, SDP PCMA/PCMU/G722
    Note over S: inbound trunk callgo-in-+97677001234<br/>(allowed_addresses = 172.28.0.10)<br/>dispatch rule callgo-rule-+97677001234
    S->>L: join room call_+97699112233_xxxx as sip_+97699112233 (psrpc via Redis)
    S-->>A: 180 Ringing
    A-->>C: 180 Ringing
    L->>B: webhook room_started
    L->>B: webhook participant_joined (kind SIP)
    Note over B: GetSIPNumberByNumber(trunkPhoneNumber)<br/>create Call{inbound, ringing, roomName}
    B-->>W: WS call.started / call.ringing
    L->>G: agent job (agent_name callgo, metadata {sipNumberId, direction})
    G->>B: GET /internal/agent/bootstrap?room=…&sipNumber=…&from=…&direction=inbound<br/>(X-Agent-Token)
    B-->>G: {call, org, profile, llm (+apiKey), llmFallbacks, lexicon, contact}
    G->>L: join room, subscribe to caller audio, publish TTS track
    Note over S: livekit-sip keeps ringing until the dispatched agent is in the room
    S-->>A: 200 OK (SDP)
    A-->>C: 200 OK (SDP: SIP_EXTERNAL_IP, RTP 10000-10200)
    C->>A: ACK
    G->>B: POST /internal/agent/events {call.answered, agent.state}
    B-->>W: WS call.answered
    loop conversation
        C->>A: RTP (G.711 A-law)
        A->>S: RTP
        S->>L: Opus (WebRTC)
        L->>G: caller audio → VAD → STT → lexicon/normalizer → LLM → TTS
        G->>L: agent audio
        L->>S: Opus
        S->>A: RTP
        A->>C: RTP
        G->>B: POST /internal/agent/events {transcript.partial/final, agent.state}
        B-->>W: WS transcript.final (persisted TranscriptTurn)
    end
    alt caller hangs up
        C->>A: BYE
        A->>S: BYE
        S->>L: leave room
        L->>B: webhook participant_left
        L->>G: participant disconnected
    else agent ends the call (end_call tool / max duration)
        G->>L: delete room / remove SIP participant
        L->>S: participant removed
        S->>A: BYE
        A->>C: BYE
    end
    G->>B: POST /internal/agent/events {call.ended: endReason, summary, sentiment, intent, durationSec, llmModelUsed}
    B-->>W: WS call.ended / call.updated
    L->>B: webhook room_finished
    Note over B: finalize Call (endedAt, status completed)
```

Failure cases on the inbound side:

| Where | What the carrier sees |
|---|---|
| No LiveKit inbound trunk for the DID | livekit-sip rejects (404 / 403) → `Hangup(${HANGUPCAUSE})` relays it |
| Asterisk IP not in `allowed_addresses` | 403 from livekit-sip |
| No agent worker online | ringing until Asterisk's `INBOUND_RING_TIMEOUT` (45 s) → no answer |
| livekit-sip unreachable | Asterisk qualify marks `livekit` unavailable → 503/480 |

---

## 2. Outbound call (manual dial or campaign) / Гарах дуудлага

```mermaid
sequenceDiagram
    autonumber
    participant W as Browser / Campaign engine
    participant B as Backend
    participant L as livekit-server
    participant S as livekit-sip
    participant A as Asterisk
    participant C as Carrier SBC
    participant G as Agent worker

    W->>B: POST /api/calls/dial {toNumber, sipNumberId}<br/>(or campaign engine ClaimTargets)
    Note over B: create Call{outbound, queued, room call-{callId}}
    B-->>W: WS call.started
    B->>L: CreateRoom call-{callId} + agent dispatch<br/>{callId, direction:outbound, toNumber, fromNumber, …}
    L->>G: agent job
    G->>B: GET /internal/agent/bootstrap?room=call-{callId}&callId=…&direction=outbound
    B-->>G: profile, llm, lexicon, contact, campaign {script, vars}
    G->>L: join room (waits for the callee)
    B->>L: CreateSIPParticipant(trunk callgo-out-{fromNumber}, sip_call_to, wait_until_answered, ringing_timeout)
    L->>S: psrpc dial request
    S->>A: INVITE sip:+97688001122@172.28.0.10 (From: +97677001234)
    A-->>S: 401 Unauthorized (endpoint livekit, auth=livekit-auth)
    S->>A: INVITE + Authorization (SIP_AUTH_USERNAME / SIP_AUTH_PASSWORD)
    Note over A: [from-livekit] E.164 → CARRIER_NUMBER_FORMAT<br/>trunk = AstDB callgo-endpoint/{fromNumber} or "carrier"
    A->>C: INVITE sip:88001122@SBC (From/PAI caller ID, SDP SIP_EXTERNAL_IP)
    B-->>W: WS call.ringing
    C-->>A: 180 Ringing
    A-->>S: 180 Ringing
    alt answered
        C-->>A: 200 OK
        A-->>S: 200 OK
        S->>L: SIP participant joins the room
        L-->>B: CreateSIPParticipant returns {participantId, sipCallId}
        L->>B: webhook participant_joined
        Note over B: Call → active (answeredAt)
        B-->>W: WS call.answered
        Note over G,C: conversation exactly as in the inbound flow
    else busy / no answer / rejected
        C-->>A: 486 Busy / 480 / 408 / 404
        A-->>S: same status (Hangup(${HANGUPCAUSE}))
        S-->>L: dial failed (SIP status)
        L-->>B: CreateSIPParticipant error (sip status code)
        Note over B: Call → busy / no_answer / failed<br/>campaign target retried up to maxAttempts
        B->>L: DeleteRoom (agent leaves)
        B-->>W: WS call.ended, campaign.progress
    end
```

---

## 3. Operator actions from the CRM / CRM-ээс хийх үйлдэл

```mermaid
sequenceDiagram
    participant W as Browser
    participant B as Backend
    participant L as livekit-server
    participant S as livekit-sip
    participant A as Asterisk
    participant C as Carrier

    W->>B: POST /api/calls/{id}/hangup
    B->>L: DeleteRoom(roomName)
    L->>S: room closed
    S->>A: BYE
    A->>C: BYE
    L->>B: webhooks participant_left, room_finished

    W->>B: POST /api/calls/{id}/transfer {toNumber}
    B->>L: TransferSIPParticipant(room, participant, tel:+976…)
    L->>S: transfer
    S->>A: REFER Refer-To: sip:+976…
    Note over A: res_pjsip_refer: blind transfer of the carrier leg<br/>into [from-livekit] (endpoint context) → Dial(...@carrier)
    A->>C: INVITE +976… (new leg), caller is bridged to it
```

---

## 4. Recording / Бичлэг (profile `recording`)

```mermaid
sequenceDiagram
    participant L as livekit-server
    participant E as egress
    participant B as Backend
    participant N as nginx (frontend)
    participant W as Browser

    Note over L: room created with room_config.egress (dispatch rule)<br/>or backend StartRoomCompositeEgress{audio_only}
    L->>E: start audio-only room composite (psrpc via Redis)
    E->>L: subscribe to all audio tracks
    Note over E: write /out/recordings/{room}-{time}.ogg<br/>(or upload to S3)
    L->>B: webhook egress_started
    Note over L,E: … call ends, room closes …
    E-->>L: egress complete (file_results[0].location)
    L->>B: webhook egress_ended {egressInfo.file_results[0].location}
    Note over B: Call.recordingUrl = S3 URL, or "/recordings/{file}" for local files
    B-->>W: WS call.updated {recordingUrl}
    W->>B: GET /api/calls/{id}/recording
    B-->>W: 302 → recordingUrl
    W->>N: GET /recordings/{file}.ogg
    N-->>W: audio/ogg from the shared `recordings` volume
```

---

## 5. Ports on the path / Замын портууд

| Hop | Transport | Ports |
|---|---|---|
| Carrier ⇄ Asterisk | SIP UDP / RTP | 5060/udp, 10000-10200/udp (public, published) |
| Asterisk ⇄ livekit-sip | SIP UDP / RTP | 172.28.0.10:5060 ⇄ 172.28.0.11:5060, RTP 10000-10200 on both containers (private) |
| livekit-sip, agent, egress ⇄ livekit-server | WebSocket + WebRTC | ws://livekit:7880, ICE UDP 50000-50100 / TCP 7881 (internal candidates) |
| livekit-sip, egress ⇄ livekit-server | psrpc | Redis `redis:6379` |
| livekit-server → backend | HTTP webhooks (JWT signed with LIVEKIT_API_KEY/SECRET) | http://backend:8080/api/livekit/webhook |
| agent → backend | HTTP, `X-Agent-Token` | http://backend:8080/internal/agent/* |
| backend → agent | HTTP (LLM test) | http://agent:8090/test-llm |
| browser → nginx → backend | HTTPS / WSS | 443 → frontend:80 → backend:8080 (`/api`, `/api/ws`) |
