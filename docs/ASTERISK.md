# CallGo.mn — Asterisk & carrier trunk / Asterisk ба операторын SIP шугам

Asterisk 22 LTS (image `andrius/asterisk:22.10.1_debian-trixie`) is CallGo's
carrier-facing PBX. It terminates the carrier trunk, normalises numbers to
E.164, and bridges calls to/from **livekit-sip**, which puts them into
LiveKit rooms where the AI agent works. Sequence diagrams: [SIP_FLOW.md](SIP_FLOW.md).

```
Carrier SBC ⇄ [endpoint carrier] Asterisk 172.28.0.10 [endpoint livekit] ⇄ livekit-sip 172.28.0.11:5060
            public 5060/udp, RTP 10000-10200            private Docker network
```

---

## 1. Тохиргооны файлууд / Configuration files

All files live in [`infra/asterisk/`](../infra/asterisk) and are mounted
read-only at `/etc/asterisk-src`. At container start
[`render-config.sh`](../infra/asterisk/render-config.sh) copies them into
`/etc/asterisk`, substituting **only** an allow-list of `${VARIABLES}` from
`.env` (dialplan variables such as `${EXTEN}` are untouched), then hands over
to the image's entrypoint.

| File | Content |
|---|---|
| `pjsip.conf` | global, UDP transport (NAT), `livekit` aor/auth/endpoint/identify, includes |
| `pjsip_carrier.conf.in` | carrier auth/aor/endpoint/identify — rendered only when `CARRIER_HOST` is set |
| `pjsip_carrier_registration.conf.in` | outbound REGISTER — only when `CARRIER_REGISTER=yes` |
| `pjsip_testphone.conf.in` | test softphone account — only when `TEST_PHONE_PASSWORD` is set |
| `extensions.conf` | dialplan: `from-carrier`, `from-livekit`, `from-testphone`, subroutines |
| `rtp.conf` | RTP ports 10000-10200 (= compose port mapping) |
| `logger.conf` | console, `messages.log`, `security.log` (fail2ban) |
| `modules.conf` | autoload minus unused channel drivers / back-ends |
| `asterisk.conf` | image directory layout, AstDB moved to the spool volume, `transmit_silence` |

Change `.env`, then `infra/scripts/compose.sh --profile full up -d asterisk`
(re-renders on restart). Hot reload of hand edits inside the container:
`asterisk -rx "module reload res_pjsip.so"` / `asterisk -rx "dialplan reload"`.

| Variable | Default | Meaning |
|---|---|---|
| `SIP_EXTERNAL_IP` | auto (full-up.sh) | public IPv4 in SIP/SDP towards the carrier |
| `SIP_AUTH_USERNAME` / `SIP_AUTH_PASSWORD` | — (required) | digest credentials livekit-sip must present (= LiveKit outbound trunk auth) |
| `CARRIER_HOST` / `CARRIER_PORT` | — / 5060 | carrier SBC |
| `CARRIER_USERNAME` / `CARRIER_PASSWORD` | — | trunk credentials (empty username = IP-authenticated trunk) |
| `CARRIER_REGISTER` | `no` | `yes` = send REGISTER |
| `CARRIER_FROM_USER` / `CARRIER_FROM_DOMAIN` | username / host | From header on outbound; `callerid` = From carries our DID |
| `CARRIER_MATCH` | `CARRIER_HOST` | carrier signalling IPs/CIDRs (comma separated) |
| `CARRIER_DID_SOURCE` | `ruri` | read the called DID from Request-URI (`ruri`) or `To` header (`to`) |
| `CARRIER_DEFAULT_DID` | — | E.164 DID when the carrier only sends the trunk username |
| `CARRIER_NUMBER_FORMAT` | `national` | outbound format: `e164`, `intl`, `national` |
| `ASTERISK_RECORD_CALLS` | `no` | MixMonitor WAV archive in `/var/spool/asterisk/monitor` |
| `TEST_PHONE_PASSWORD` | — | enables the `testphone` account (≥ 12 chars) |

---

## 2. Операторын бүртгэл / Carrier trunk and registration

Ask the carrier (Unitel, Mobicom, Skytel, Univision, G-Mobile, …) for:

1. **SBC signalling address(es)** and port, UDP/TCP — `CARRIER_HOST`, `CARRIER_PORT`, `CARRIER_MATCH`.
2. **Media IP range** — to open 10000-10200/udp for it.
3. **Authentication model**: registration (username + password) or IP
   whitelisting (give them `SIP_EXTERNAL_IP`).
4. **DID list** and how it is presented in inbound INVITEs (Request-URI or
   `To`; 8-digit national `77001234`, `97677001234` or `+97677001234`).
5. **Caller-ID rules** for outbound: which header (From vs
   P-Asserted-Identity) and which format.
6. Codec (normally G.711 A-law), DTMF (RFC 2833/4733), session timers,
   max channels.

### Registration trunk / Бүртгэлтэй шугам
```ini
# .env
CARRIER_HOST=sbc.carrier.mn
CARRIER_USERNAME=77001234
CARRIER_PASSWORD=********
CARRIER_REGISTER=yes
CARRIER_DID_SOURCE=to            # INVITEs are addressed to the username, DID is in To
CARRIER_DEFAULT_DID=+97677001234 # single-DID trunks
```
Check: `asterisk -rx "pjsip show registrations"` → `Registered`. The
registration uses `line=yes`, so inbound INVITEs match the `carrier`
endpoint even from SBC addresses not listed in `CARRIER_MATCH`.

### IP-authenticated trunk / IP-ээр баталгаажсан шугам
```ini
CARRIER_HOST=10.20.30.40
CARRIER_MATCH=10.20.30.40,10.20.30.41
CARRIER_USERNAME=                # empty: no auth object, no REGISTER
CARRIER_REGISTER=no
CARRIER_DID_SOURCE=ruri
```

---

## 3. DID routing / Дугаарын чиглүүлэлт

### Inbound (`[from-carrier]`)
1. `DID` = Request-URI user, or the `To` user when `CARRIER_DID_SOURCE=to`.
   Anything that is not a phone number (e.g. the trunk username) falls back
   to `CARRIER_DEFAULT_DID`.
2. `DID` and the caller ID go through `sub-e164`:

   | Received | Normalised |
   |---|---|
   | `77001234` (8 digits) | `+97677001234` |
   | `97677001234` | `+97677001234` |
   | `0097677001234` | `+97677001234` |
   | `+79161234567` | unchanged |
   | `anonymous` | empty |

3. `Dial(PJSIP/<E.164 DID>@livekit)` with pre-dial headers `X-CallGo-DID` and
   `X-CallGo-Source`. livekit-sip matches the **inbound trunk whose numbers
   contain that E.164 DID**, then its **dispatch rule** creates room
   `call_<caller>_<random>` and dispatches agent `callgo`.
4. Asterisk does **not** answer by itself: the carrier sees ringing until
   livekit-sip answers (after the agent joined), and the final SIP status
   (busy, unavailable…) is propagated with `Hangup(${HANGUPCAUSE})`.

### Outbound (`[from-livekit]`)
livekit-sip sends `INVITE sip:<callee>@172.28.0.10` with `From: <our DID>`,
authenticating with `SIP_AUTH_USERNAME/PASSWORD`. The dialplan normalises
both numbers, picks the trunk (§4), formats them with `sub-carrier-format`
and dials `PJSIP/<number>@<trunk>`:

| `CARRIER_NUMBER_FORMAT` | `+97688001122` becomes | foreign `+79161234567` |
|---|---|---|
| `e164` | `+97688001122` | `+79161234567` |
| `intl` | `97688001122` | `79161234567` |
| `national` | `88001122` | `0079161234567` |

The carrier's final status (486 busy, 480/408 no answer, 404 …) is passed
back to livekit-sip so the CallGo dialer can classify the outcome.

Test extensions: `9000` = echo, `9001` = 1004 Hz milliwatt tone
(`infra/scripts/sip-test-call.sh 9000`).

---

## 4. CRM SIP number ↔ Asterisk

| CRM field (`SIPNumber`) | Where it is used |
|---|---|
| `number` (E.164) | LiveKit inbound trunk `numbers`, outbound trunk `numbers` (caller ID); the DID Asterisk dials into `@livekit` after normalisation |
| `inboundTrunkId`, `dispatchRuleId`, `outboundTrunkId` | LiveKit objects `callgo-in-<number>`, `callgo-rule-<number>`, `callgo-out-<number>` created by the backend (or `lk-setup.sh`) |
| `asteriskEndpoint` | name of the PJSIP **trunk endpoint** the number belongs to — `carrier` by default |
| `allowInbound` / `allowOutbound` | whether the inbound trunk + rule / outbound trunk exist |

Single carrier: leave `asteriskEndpoint` empty or `carrier`. Several carriers:

1. Copy `pjsip_carrier.conf.in` into a second block with the names
   `carrier2`/`carrier2-auth` (its identify match = the second carrier's IPs);
   inbound calls from it land in `[from-carrier]` automatically.
2. In the CRM set the number's `asteriskEndpoint` to `carrier2`.
3. Map outbound calls of that number to the trunk (AstDB, stored in
   `/var/spool/asterisk/astdb` on the `asterisk-spool` volume, so it survives
   container re-creation):
   ```bash
   asterisk -rx "database put callgo-endpoint +97677009999 carrier2"
   asterisk -rx "database show callgo-endpoint"
   ```
   `[from-livekit]` looks up `callgo-endpoint/<caller E.164>` and falls back
   to `carrier`. (Automating this from the backend over AMI/ARI is a possible
   follow-up; the CRM field already carries the value.)

---

## 5. Codec, DTMF, NAT / Кодек, DTMF, NAT

* **Codecs**: carrier leg `alaw,ulaw`; livekit leg `alaw,ulaw,g722`
  (livekit-sip supports PCMA/PCMU/G.722 — no Opus on its SIP side). With
  G.711 A-law on both legs Asterisk relays media without transcoding;
  livekit-sip converts to Opus for the room.
* **DTMF**: `dtmf_mode=rfc4733` on both legs → digits pass through
  transparently and reach the agent as LiveKit SIP DTMF events.
* **NAT**: Asterisk sits on a Docker bridge with published ports.
  `external_signaling_address`/`external_media_address` = `SIP_EXTERNAL_IP`
  for peers outside `local_net` (172.28.0.0/24); `rtp_symmetric`,
  `force_rport`, `rewrite_contact` handle carriers behind NAT;
  `strictrtp=yes` learns the real media source. The published RTP range must
  equal `rtp.conf`. For very high call volumes, Asterisk can instead run
  with `network_mode: host` (then livekit-sip must use another port than 5060,
  and `SIP_ASTERISK_HOST`/identify addresses change to the host IP).
* **Security**: no anonymous endpoint (`endpoint_identifier_order=ip,username`);
  livekit-sip must pass digest auth; restrict 5060/udp to carrier IPs in the
  `DOCKER-USER` chain ([DEPLOY.md §3](DEPLOY.md#3-порт-ба-галт-хана--ports-and-firewall));
  point fail2ban's `asterisk` jail at `security.log` in the `asterisk-log` volume.

---

## 6. Troubleshooting / Алдаа засах

Open the CLI: `infra/scripts/compose.sh --profile full exec asterisk asterisk -rvvv`

| Command | Shows |
|---|---|
| `pjsip show endpoints` | endpoints, auth, contacts and their qualify status |
| `pjsip show aors` / `pjsip show contacts` | is livekit-sip / the carrier reachable (`Avail`, RTT) |
| `pjsip show registrations` | carrier REGISTER state |
| `pjsip show identifies` | which source IPs map to which endpoint |
| `pjsip set logger on` (`… host 10.20.30.40`) | full SIP trace (chan_sip's `sip set debug` does not exist in Asterisk 21+) |
| `rtp set debug on` | RTP packets per peer (one-way audio) |
| `core show channels verbose` | live calls and the dialplan step they are in |
| `dialplan show from-carrier` | loaded dialplan |
| `database show callgo-endpoint` | per-number trunk overrides |

Other sides: `make compose-logs SERVICE=livekit-sip` (set `logging.level: debug`
in `infra/livekit-sip/config.yaml` for SIP dumps), `make lk-setup ARGS=--list`,
`lk room list`.

| Symptom | Likely cause / fix |
|---|---|
| `No matching endpoint found` in the log | source IP not in `CARRIER_MATCH` / not the livekit-sip IP |
| Registration `Rejected` / 403 | wrong `CARRIER_USERNAME/PASSWORD`, or carrier expects a different `client_uri` domain (`CARRIER_FROM_DOMAIN`) |
| Inbound → livekit answers 404 / `no trunk` | the E.164 DID is not on any LiveKit inbound trunk: add the number in the CRM or `lk-setup.sh -n +976…`; check `CARRIER_DID_SOURCE` |
| Inbound → 403 from livekit-sip | Asterisk's IP not in the trunk `allowed_addresses` (`SIP_ALLOWED_ADDRESSES=172.28.0.10/32`) |
| Inbound rings, never answered | no agent worker registered as `callgo` (livekit-sip waits for the agent) |
| Outbound → 401 loop | `SIP_AUTH_*` in the LiveKit outbound trunk ≠ Asterisk `livekit-auth`; re-provision the number after changing `.env` |
| Outbound → 404 / 484 from carrier | wrong `CARRIER_NUMBER_FORMAT`, or carrier wants the caller ID in From (`CARRIER_FROM_USER=callerid`) |
| One-way or no audio | `SIP_EXTERNAL_IP` wrong/empty, RTP range blocked, carrier media IPs not allowed |
| Calls drop after ~30-60 s | NAT/firewall timing out RTP or missing ACK: check `rtp_timeout`, session timers, `SIP_EXTERNAL_IP` in Contact |
| DTMF not detected | carrier sends in-band or SIP INFO: set `dtmf_mode=auto` (or `info`) on the carrier endpoint |

---

## 7. Test softphone / Туршилтын softphone

```ini
TEST_PHONE_PASSWORD=long-random-secret
CARRIER_DEFAULT_DID=+97677001234
```
Restart asterisk, allow your IP to 5060/udp, register a softphone as user
`testphone`, server `<SIP_EXTERNAL_IP>:5060` (UDP). Any number dialled from it
enters `[from-carrier]` as a call to `CARRIER_DEFAULT_DID` with caller ID
`+97699000000` — the full inbound path without a carrier. Remove the
password in production.
