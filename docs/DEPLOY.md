# CallGo.mn — Deployment guide / Байршуулах заавар

This guide takes a fresh **Ubuntu 24.04** VPS to a working CallGo.mn instance
that answers real phone calls with the AI agent. Everything runs from one
`docker compose` file ([`infra/docker-compose.yml`](../infra/docker-compose.yml));
TLS is terminated by Caddy on the host.

Related documents: [ASTERISK.md](ASTERISK.md) (carrier trunk, dialplan,
troubleshooting), [SIP_FLOW.md](SIP_FLOW.md) (sequence diagrams),
[ARCHITECTURE.md](ARCHITECTURE.md), [API.md](API.md).

---

## 1. Ерөнхий бүтэц / Overview

```
                      Internet
   ┌───────────────┬──────────────┬──────────────────────────────┐
   │ 443/tcp       │ 5060/udp     │ 10000-10200/udp               │ 50000-50100/udp, 7881/tcp
   ▼               ▼              ▼                               ▼
 Caddy (host) ── frontend:80 ── /api ──▶ backend:8080 ──▶ postgres:5432
   TLS            nginx + SPA            │   ▲  webhooks
                                         │   └──────────── livekit:7880 ◀──▶ redis:6379
                                         │ internal API          ▲   ▲
                                         ▼                       │   │ psrpc (redis)
                                       agent:8090 ── WebRTC ─────┘   │
                                                                 livekit-sip:5060  (172.28.0.11)
                                                                     ▲ SIP/RTP (private network)
                          Carrier SBC ──SIP/RTP──▶ asterisk:5060 ────┘ (172.28.0.10)
```

| Service | Image (pinned in `.env`) | Profile | Purpose |
|---|---|---|---|
| postgres | `postgres:16-alpine` | dev, full | CRM database (`callgo`, plus `callgo_test`) |
| redis | `redis:7.4-alpine` | dev, full | LiveKit bus shared by livekit, livekit-sip, egress |
| livekit | `livekit/livekit-server:v1.13` | dev, full | Rooms / SFU / SIP + agent-dispatch control plane |
| livekit-sip | `livekit/sip:v1.17` | full | SIP ⇄ LiveKit bridge (talks only to Asterisk) |
| asterisk | `andrius/asterisk:22.10.1_debian-trixie` | full | Carrier-facing PBX (Asterisk 22 LTS) |
| egress | `livekit/egress:v1.14` | recording | Call recordings (audio-only room composite) |
| backend | built from `backend/` | dev, full | Go API, webhooks, dialer |
| agent | built from `agent/` | dev, full | Python voice-AI worker (STT → LLM → TTS) |
| frontend | built from `frontend/` | dev, full | nginx serving the SPA, proxying `/api` + `/api/ws` |

Profiles: **dev** = everything except telephony (mock calls), **full** = dev
+ livekit-sip + Asterisk (real calls), **recording** = add egress.

---

## 2. Серверийн шаардлага / Server requirements

| | Minimum | Recommended |
|---|---|---|
| OS | Ubuntu 24.04 LTS x86_64 | same |
| CPU / RAM | 8 vCPU / 16 GB | 16 vCPU / 32 GB, or 8 vCPU + GPU |
| Disk | 80 GB SSD | 200 GB SSD (models ≈ 5 GB, recordings grow) |
| Network | 1 public static IPv4, no carrier-grade NAT | same, low latency to the carrier SBC |
| GPU (optional) | — | NVIDIA T4 / L4 / A10 (16 GB+) for `large-v3` Whisper |

Rough capacity guidance (benchmark with your own audio): on CPU only, prefer
`CALLGO_WHISPER_MODEL=large-v3-turbo` or `medium` with `int8` and expect a
handful of simultaneous calls on 8 vCPU; a single 16 GB GPU runs `large-v3`
in `float16` for considerably more concurrent calls. LLM and cloud TTS
latency is usually the larger share of response time.

Each call uses ~4 LiveKit UDP ports (range 50000-50100 ≈ 25 calls) and 2
Asterisk RTP ports (10000-10200 ≈ 100 calls). Widen both ranges together
with the compose port mappings for more.

---

## 3. Порт ба галт хана / Ports and firewall

| Port | Proto | Service | Open to |
|---|---|---|---|
| 22 | tcp | SSH | your admin IPs |
| 80, 443 | tcp | Caddy (TLS, ACME) | everyone |
| 5060 | udp | Asterisk SIP signalling | **carrier SBC IPs only** |
| 10000-10200 | udp | Asterisk RTP media | carrier media IPs (or everyone if unknown) |
| 7881 | tcp | LiveKit ICE/TCP fallback | everyone (only needed for external WebRTC clients) |
| 7882 | udp | LiveKit UDP mux (only if `rtc.udp_port` enabled) | everyone |
| 50000-50100 | udp | LiveKit WebRTC media | everyone (only needed for external WebRTC clients) |
| 7880 | tcp | LiveKit API/signalling | loopback by default (`LIVEKIT_HTTP_PUBLISH`); expose via Caddy `wss://` if needed |
| 8080, 5432, 6379 | tcp | backend / postgres / redis | loopback only (defaults) |
| 8088 | tcp | frontend nginx | loopback when Caddy is in front (`FRONTEND_PUBLISH=127.0.0.1:8088`) |

In the default topology the AI agent and livekit-sip reach LiveKit over the
private Docker network, so 7881/50000-50100 only matter when browsers or
remote agent workers connect to LiveKit directly.

**Docker bypasses UFW.** Ports published by Docker are inserted into
iptables *before* UFW's rules, so `ufw deny 5060` does nothing for
containers. Restrict published ports in the `DOCKER-USER` chain instead:

```bash
# Allow SIP only from the carrier SBC(s); drop everyone else (replace IPs).
sudo iptables -I DOCKER-USER -p udp --dport 5060 -j DROP
sudo iptables -I DOCKER-USER -p udp --dport 5060 -s 202.131.0.10 -j RETURN   # carrier SBC #1
sudo iptables -I DOCKER-USER -p udp --dport 5060 -s 202.131.0.11 -j RETURN   # carrier SBC #2
# (test softphone: temporarily add your own IP the same way)
sudo apt install -y iptables-persistent && sudo netfilter-persistent save
```

Host (non-Docker) ports with UFW:

```bash
sudo ufw allow OpenSSH && sudo ufw allow 80,443/tcp && sudo ufw enable
```

---

## 4. Docker суулгах / Install Docker

```bash
sudo apt update && sudo apt install -y ca-certificates curl git make python3 gettext-base
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER" && newgrp docker
docker compose version            # v2.24+ required
```

Recommended `/etc/docker/daemon.json` (large UDP ranges without one
docker-proxy process per port, bounded logs), then `sudo systemctl restart docker`:

```json
{
  "userland-proxy": false,
  "log-driver": "json-file",
  "log-opts": { "max-size": "20m", "max-file": "5" }
}
```

GPU hosts additionally need the NVIDIA driver and `nvidia-container-toolkit`
(`sudo nvidia-ctk runtime configure --runtime=docker`).

---

## 5. Алхам алхмаар суулгах / Step by step

### 5.1 Clone / Кодыг татах
```bash
git clone https://github.com/orgilts603/callgo.mn-v2.git /opt/callgo
cd /opt/callgo
```

### 5.2 `.env` үүсгэх / Create the environment file
```bash
make env ENV_ARGS=--prod     # writes .env (mode 600) with random secrets, prints the admin password
```
Then edit `.env` (every variable is documented in [`.env.example`](../.env.example)):

| Variable | Set to |
|---|---|
| `SIP_EXTERNAL_IP` | the VPS public IPv4 (auto-detected by `full-up.sh` if empty) |
| `CARRIER_HOST`, `CARRIER_PORT` | the carrier SBC (from the carrier) |
| `CARRIER_USERNAME`, `CARRIER_PASSWORD`, `CARRIER_REGISTER` | trunk credentials; `yes` for registration trunks |
| `CARRIER_MATCH` | all carrier signalling IPs, comma separated |
| `CARRIER_DID_SOURCE`, `CARRIER_DEFAULT_DID`, `CARRIER_NUMBER_FORMAT` | see [ASTERISK.md §3](ASTERISK.md#3-did-routing--дугаарын-чиглүүлэлт) |
| `SIP_NUMBERS` | your DIDs in E.164 (`+97677001234`), used by the helper scripts |
| `OPENAI_API_KEY` … | optional default LLM keys (per-org keys are entered in the UI) |
| `CALLGO_RECORDING` | `true` to run LiveKit egress (§7) |
| `CALLGO_GPU` | `true` on GPU hosts |

Keep `CALLGO_ENCRYPTION_KEY` safe and stable: it encrypts the LLM API keys
stored in PostgreSQL. `POSTGRES_PASSWORD` is applied only when the database
volume is first created.

### 5.3 Загвар татах / Download models
```bash
make models      # faster-whisper model + Piper voices into agent/models (bind-mounted at /models)
```
There is no public Mongolian Piper voice; copy your trained
`mn_MN-….onnx` + `.onnx.json` into `agent/models/piper/` (name =
`CALLGO_PIPER_DEFAULT_VOICE`) or choose a cloud TTS in the agent profile.
`make models` needs the agent virtualenv (`cd agent && uv sync`) or
`faster-whisper` installed; otherwise the agent downloads models on first use.

### 5.4 Асаах / Start the stack
```bash
make full-up                 # = infra/scripts/full-up.sh (pre-flight checks, builds, waits for health)
make compose-ps              # every service "running"/"healthy"
```
`full-up.sh` forces `CALLGO_MOCK_TELEPHONY=false`, adds the `recording`
profile when `CALLGO_RECORDING=true` and the GPU override when `CALLGO_GPU=true`.
For a demo without telephony use `make dev-up` instead.

### 5.5 TLS / DNS
Point an `A` record (e.g. `crm.example.mn`) at the server, set
`FRONTEND_PUBLISH=127.0.0.1:8088` in `.env` (already done by `--prod`), and:
```bash
sudo apt install -y caddy
sudo cp infra/caddy/Caddyfile /etc/caddy/Caddyfile   # edit the domain
sudo systemctl reload caddy
```
Caddy obtains a Let's Encrypt certificate and proxies the `/api/ws`
WebSocket automatically. (nginx + certbot works equally; proxy `/` to
`127.0.0.1:8088` with the WebSocket `Upgrade`/`Connection` headers.)

### 5.6 Утасны хавтгайг шалгах / Verify the telephony plane
```bash
C="infra/scripts/compose.sh --profile full"
$C exec asterisk asterisk -rx "pjsip show registrations"   # carrier: Registered (registration trunks)
$C exec asterisk asterisk -rx "pjsip show aors"            # livekit contact: Avail
$C logs --tail=50 livekit-sip                               # "sip signaling listening on ... 5060"
```

### 5.7 (Optional) `lk-setup` — LiveKit SIP bootstrap
```bash
make lk-setup                         # trunks + dispatch rule for every SIP_NUMBERS entry
make lk-setup ARGS="--dry-run"        # print the rendered requests only
```
It creates `callgo-in-<number>`, `callgo-rule-<number>` and
`callgo-out-<number>` from [`infra/livekit/sip/*.json`](../infra/livekit/sip).
These are the **same names the backend uses**, so when you later add the
number in the CRM the backend adopts and updates them — no duplicates. Use it
to test the phone path before the CRM is configured; otherwise §5.9 is enough.
Uses the host `lk` CLI when installed, else the `livekit/livekit-cli` image.

### 5.8 Анхны нэвтрэлт / First login
Open `https://crm.example.mn`, log in as `CALLGO_ADMIN_EMAIL` with the
password printed by `make env`. Then:
1. **Settings → LLM configs**: add a provider (OpenAI / Anthropic / Google /
   Groq / Ollama / OpenAI-compatible), press **Test**.
2. **Agent profiles**: system prompt, greeting (Mongolian), STT
   (`faster_whisper`), TTS voice, tools, transfer number.

### 5.9 SIP дугаар нэмэх / Add a SIP number in the UI
**Settings → SIP numbers → Add**:

| Field | Example | Meaning |
|---|---|---|
| Number | `+97677001234` | the DID in E.164 — must equal what Asterisk dials (`PJSIP/<E.164>@livekit`) |
| Label | `Hotline` | free text |
| Agent profile | `Reception (mn)` | who answers inbound calls |
| Allow inbound / outbound | ✓ / ✓ | creates the inbound trunk + dispatch rule / outbound trunk |
| Asterisk endpoint | `carrier` | PJSIP trunk the number lives on ([ASTERISK.md §4](ASTERISK.md#4-crm-sip-number--asterisk)) |

Saving calls `Telephony.EnsureNumberProvisioned`: the backend creates the
LiveKit inbound trunk (allowed address = Asterisk 172.28.0.10), the
individual dispatch rule (rooms `call_<caller>_<random>`, agent `callgo`)
and the outbound trunk (→ `172.28.0.10:5060`, digest `SIP_AUTH_*`). Check:
```bash
make lk-setup ARGS=--list
```

### 5.10 Туршилтын дуудлага / Test calls
1. **Echo (no carrier)**: `make sip-test-call TO=9000 ARGS=--no-agent` →
   livekit-sip → Asterisk echo; proves SIP auth + media between them.
2. **Softphone as fake PSTN**: set `TEST_PHONE_PASSWORD` (≥ 12 chars) and
   `CARRIER_DEFAULT_DID`, restart asterisk, register a softphone as
   `testphone@<SIP_EXTERNAL_IP>` (allow your IP in `DOCKER-USER`), dial
   anything → the AI answers; the call appears live in the **Live Desk**.
3. **Real inbound**: call your DID from a mobile phone.
4. **Real outbound**: **Calls → Dial** in the UI (`POST /api/calls/dial`) or
   `make sip-test-call TO=+97699112233`.

---

## 6. NAT ба гадаад IP / External IP configuration

* **Asterisk** is the only component that talks to the carrier. It runs on the
  Docker bridge; `external_signaling_address`/`external_media_address` =
  `SIP_EXTERNAL_IP` are written into SIP/SDP for peers outside
  `local_net` (the carrier), while livekit-sip (inside `local_net`
  172.28.0.0/24) sees the container IP.
* **livekit-sip** only talks to Asterisk over the private network, therefore
  `use_external_ip: false` ([config](../infra/livekit-sip/config.yaml)) and no
  published ports — it also cannot clash with Asterisk on host port 5060.
  If you ever connect a carrier **directly** to livekit-sip (no Asterisk), run
  it with `network_mode: host`, set `use_external_ip: true`, open 5060 and its
  RTP range, and point `ws_url`/`redis` at the host-published ports.
* **livekit-server** discovers the public IP with STUN (`use_external_ip`) and
  also advertises its internal IP (`advertise_internal_ip`) so containers
  connect directly. If STUN is blocked, set `use_external_ip: false` and
  `node_ip: <public IP>` in [`livekit.yaml`](../infra/livekit/livekit.yaml).

---

## 7. Бичлэг / Call recording (optional)

```bash
# .env: CALLGO_RECORDING=true   (then)
make full-up
make lk-setup ARGS=--recreate    # only for numbers managed by lk-setup: adds room_config.egress
```
* Egress writes `/out/recordings/<room>-<time>.ogg` to the `recordings`
  volume; the frontend nginx serves it at `/recordings/<file>`.
* When the room ends LiveKit sends `egress_ended` to `POST /api/livekit/webhook`;
  the backend stores `EgressInfo.file_results[0].location` as
  `Call.recordingUrl` (a local `/out/recordings/x.ogg` becomes `/recordings/x.ogg`),
  and `GET /api/calls/{id}/recording` redirects to it.
* Production: configure `storage.s3` in [`infra/egress/egress.yaml`](../infra/egress/egress.yaml)
  (AWS S3, MinIO, R2) so recordings are not publicly guessable URLs on the VPS.
* Asterisk can additionally keep a PBX-side WAV archive (`ASTERISK_RECORD_CALLS=yes`,
  volume `asterisk-spool`, `/var/spool/asterisk/monitor`).

---

## 8. Ажиллагаа / Operations

| Task | Command |
|---|---|
| Logs | `make compose-logs SERVICE=backend` |
| Restart one service | `infra/scripts/compose.sh --profile full restart agent` |
| Asterisk console | `infra/scripts/compose.sh --profile full exec asterisk asterisk -rvvv` |
| DB backup | `infra/scripts/compose.sh --profile full exec -T postgres pg_dump -U callgo callgo \| gzip > callgo-$(date +%F).sql.gz` |
| DB restore | `gunzip -c dump.sql.gz \| infra/scripts/compose.sh --profile full exec -T postgres psql -U callgo callgo` |
| Apply migrations manually | `make migrate` (the backend also migrates on start) |
| Upgrade | `git pull && make full-up` (image tags are pinned in `.env`; bump deliberately) |
| Stop | `make compose-down` (volumes survive; `docker volume ls \| grep callgo`) |

Back up: the `callgo_postgres-data` volume (or dumps), `.env`, `agent/models/piper`
(custom voices) and recordings.

---

## 9. Өргөтгөх / Scaling

* **Agent workers scale horizontally.** Each worker registers with LiveKit
  under agent name `callgo`; LiveKit load-balances jobs.
  `infra/scripts/compose.sh --profile full up -d --scale agent=3`, or run
  workers on other (GPU) hosts with `LIVEKIT_URL=wss://rtc.example.mn`,
  `CALLGO_BACKEND_URL=https://crm.example.mn` and the same
  `CALLGO_AGENT_TOKEN` / LiveKit keys (expose LiveKit via Caddy and open
  7881/tcp + 50000-50100/udp for them).
* **GPU for Whisper**: `CALLGO_GPU=true` (uses `infra/docker-compose.gpu.yml`,
  `CALLGO_WHISPER_DEVICE=cuda`, `float16`); one GPU is shared by all agent
  processes on that host.
* **More concurrent calls**: widen `rtc.port_range_*` (+ compose mapping),
  Asterisk `rtp.conf` (+ mapping), and `CALLGO_CAMPAIGN_MAX_CONCURRENCY`.
* **Beyond one VPS**: managed PostgreSQL, a dedicated Redis, several
  livekit-server nodes sharing Redis, and several livekit-sip nodes (each
  with its own IP, listed as extra `contact=` lines on Asterisk's `livekit`
  AOR and in its `identify` match).

---

## 10. Түгээмэл асуудал / Troubleshooting

| Symptom | Check |
|---|---|
| `backend` unhealthy | `make compose-logs SERVICE=backend` — prod refuses dev secrets; DB password vs. existing volume |
| Inbound call: busy / 404 immediately | number not provisioned in LiveKit (`make lk-setup ARGS=--list`), DID not E.164 → [ASTERISK.md §6](ASTERISK.md#6-troubleshooting--алдаа-засах) |
| Inbound call rings forever | no agent worker registered: `make compose-logs SERVICE=agent` (livekit-sip keeps ringing until the `callgo` agent joins) |
| Outbound 401/403 | `SIP_AUTH_USERNAME/PASSWORD` differ between backend trunk and Asterisk — restart both after changing `.env` |
| One-way / no audio | `SIP_EXTERNAL_IP`, RTP ports 10000-10200/udp open, `DOCKER-USER` rules |
| Live Desk not updating | WebSocket through the proxy: `/api/ws` must be upgraded (Caddy does it automatically) |
| Webhooks not arriving | `make compose-logs SERVICE=livekit` shows webhook errors; `LIVEKIT_WEBHOOK_URL`, key pair equality |
