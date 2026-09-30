# CallGo.mn — SaaS Sprint Plan

Goal: turn the single-tenant demo into a self-serve, metered, billable SaaS
that a Mongolian business can sign up for, pay for (QPay), and run without us.

## Workstreams (parallel agents)

| # | Workstream | Deliverable |
|---|---|---|
| B1a | Identity & billing persistence | migrations 000005; repos for invitations, password resets, refresh sessions, API keys, audit log, subscriptions, usage, invoices, payments |
| B1b | Ops persistence | migrations 000006; repos for webhooks + deliveries, callbacks, SIP routing config, recording fields, post-call actions |
| B2 | Identity service | signup (org + owner, 14-day trial), email verify, invites, password reset, refresh-token rotation, API keys, audit, mailer port (SMTP + log) |
| B3 | Billing | plan catalog, subscription lifecycle, usage metering, quota enforcement, monthly invoices, QPay adapter (+mock), payment webhook |
| B4 | Recordings & operator handoff | LiveKit egress → S3/MinIO/local, signed URLs, retention; operator join token + agent yield; MinIO in compose |
| B5 | Integrations | outbound webhooks (HMAC, retries, delivery log), SMS gateway port (+mock), post-call action runner |
| B6 | Routing & callbacks | inbound routing (business hours, after-hours, DTMF menu), scheduled callbacks dialer |
| B7 | Analytics | outcome/profile/hour/cost queries + CSV export |
| B8 | Platform admin & CI | /api/admin (orgs, plans, usage), GitHub Actions, Playwright e2e smoke |
| P1 | Agent worker | usage metrics in call.ended, operator handoff behaviour, DTMF menu + after-hours |
| F1 | Auth & org UI | signup/verify/invite/reset pages, members, API keys, audit log, token refresh in api client |
| F2 | Billing UI | plans, subscription, usage meters, invoices, QPay QR payment, quota banners |
| F3 | Integrations & routing UI | webhooks, SMS, post-call actions, SIP routing editor, callbacks |
| F4 | Analytics, admin, operator console | analytics page, platform admin, recording player (signed URL), take-over console (livekit-client) |

## Plans (MNT, VAT excluded)

| Code | Name | Monthly | Included minutes | Concurrent calls | Profiles | Users | KB | Overage /min |
|---|---|---|---|---|---|---|---|---|
| trial | Туршилт (14 хоног) | 0 | 100 | 2 | 1 | 2 | 10 MB | — (blocked) |
| starter | Starter | 290 000 | 1 000 | 3 | 3 | 5 | 50 MB | 350 |
| growth | Growth | 890 000 | 4 000 | 10 | 10 | 15 | 500 MB | 300 |
| enterprise | Enterprise | custom | custom | custom | ∞ | ∞ | ∞ | custom |

## Sequencing

1. Contracts (domain, API.md, types.ts, schemas.py) — integrator, before agents start.
2. All 14 agents in parallel on disjoint paths (see docs/AGENT_RULES.md).
3. Integrator: wire cmd/server, mount routers, run all suites, smoke test signup → pay → call → invoice.
4. Merge to main; deploy to staging VPS; real-trunk measurement sprint follows.

## Out of scope this sprint
Marketing site, mobile app, multi-region, SSO/SAML, on-prem packaging.
