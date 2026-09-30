// Test fixtures for the billing feature (imported only by *.test.tsx).
import type { Invoice, Payment, Plan, Subscription, UsageSummary } from '@/lib/types'
import type { SubscriptionResponse } from './hooks'

const base: Omit<Plan, 'code' | 'name'> = {
  monthlyMnt: 0, includedMinutes: 0, overageMntPerMin: 0, maxConcurrentCalls: 0, maxAgentProfiles: 0, maxUsers: 0,
  maxKnowledgeMb: 0, maxSipNumbers: 0, features: [], trialDays: 0, public: true,
}
export const plans: Plan[] = [
  { ...base, code: 'trial', name: 'Туршилт', includedMinutes: 100, maxConcurrentCalls: 2, maxAgentProfiles: 1, maxUsers: 2, maxKnowledgeMb: 10, maxSipNumbers: 1, trialDays: 14 },
  { ...base, code: 'starter', name: 'Starter', monthlyMnt: 290_000, includedMinutes: 1000, overageMntPerMin: 350, maxConcurrentCalls: 3, maxAgentProfiles: 3, maxUsers: 5, maxKnowledgeMb: 50, maxSipNumbers: 2, features: ['recordings'] },
  { ...base, code: 'growth', name: 'Growth', monthlyMnt: 890_000, includedMinutes: 4000, overageMntPerMin: 300, maxConcurrentCalls: 10, maxAgentProfiles: 10, maxUsers: 15, maxKnowledgeMb: 500, maxSipNumbers: 5, features: ['recordings', 'webhooks', 'analytics'] },
  { ...base, code: 'enterprise', name: 'Enterprise', features: ['recordings', 'webhooks', 'sms', 'api', 'analytics', 'handoff', 'priority_support'] },
  { ...base, code: 'internal', name: 'Hidden', public: false },
]
export const planBy = (code: string) => plans.find((p) => p.code === code)!

export function makeSubscription(over: Partial<Subscription> = {}): Subscription {
  return {
    id: 's1', orgId: 'o1', planCode: 'starter', status: 'active', currentPeriodStart: '2026-09-01T00:00:00Z', currentPeriodEnd: '2026-10-01T00:00:00Z',
    trialEndsAt: null, canceledAt: null, createdAt: '2026-09-01T00:00:00Z', updatedAt: '2026-09-01T00:00:00Z', ...over,
  }
}
export function makeUsage(over: Partial<UsageSummary> = {}): UsageSummary {
  return {
    orgId: 'o1', periodStart: '2026-09-01T00:00:00Z', periodEnd: '2026-10-01T00:00:00Z', calls: 120, minutes: 500, includedMinutes: 1000,
    overageMinutes: 0, overageMnt: 0, llmTokens: 1_234_567, sttSeconds: 30_000, ttsChars: 98_765, sms: 12, costMnt: 45_000, ...over,
  }
}
export function makeSubResponse(opts: { sub?: Partial<Subscription>; usage?: Partial<UsageSummary>; plan?: string } = {}): SubscriptionResponse {
  const plan = planBy(opts.plan ?? opts.sub?.planCode ?? 'starter')
  return { subscription: makeSubscription({ planCode: plan.code, ...opts.sub }), plan, limits: plan, usage: makeUsage(opts.usage) }
}
export function makeInvoice(over: Partial<Invoice> = {}): Invoice {
  return {
    id: 'inv1', orgId: 'o1', number: 'INV-2026-0001', periodStart: '2026-09-01T00:00:00Z', periodEnd: '2026-10-01T00:00:00Z',
    lines: [{ description: 'Growth багц', quantity: 1, unitMnt: 890_000, amountMnt: 890_000 }],
    subtotalMnt: 890_000, vatMnt: 89_000, totalMnt: 979_000, status: 'open', dueAt: '2026-09-08T00:00:00Z', paidAt: null, createdAt: '2026-09-01T00:00:00Z', ...over,
  }
}
export function makePayment(over: Partial<Payment> = {}): Payment {
  return {
    id: 'pay1', orgId: 'o1', invoiceId: 'inv1', provider: 'qpay', providerRef: 'qp-1', amountMnt: 979_000, status: 'pending',
    qrText: '0002010102121531279404962794049600022310027138152045734530349654031005802MN5904TEST6011ULAANBAATAR', deepLinks: [
      { name: 'Khan bank', logo: 'https://qpay.mn/q/logo/khanbank.png', link: 'khanbank://q?qPay_QRcode=abc' },
      { name: 'Golomt bank', logo: '', link: 'golomtbank://q?qPay_QRcode=abc' },
    ],
    expiresAt: null, paidAt: null, createdAt: new Date().toISOString(), ...over,
  }
}
