// Non-component helpers for the identity settings tabs (members, API keys, audit, organization).
import { roleLabel } from '@/app/auth'
import type { Role, User } from '@/lib/types'

export function roleOptions(myRole: Role | undefined): { value: Role; label: string }[] {
  const roles: Role[] = myRole === 'owner' ? ['owner', 'admin', 'operator'] : ['admin', 'operator']
  return roles.map((r) => ({ value: r, label: roleLabel(r) }))
}

export interface MemberGuard { canManage: boolean; reason?: string }

/**
 * Mirrors the backend rules so the UI never offers an action that will be refused:
 * nobody edits themselves, the last active owner cannot be demoted/disabled/removed,
 * only owners touch other owners, operators manage nobody.
 */
export function memberGuard(member: User, me: User | null, activeOwners: number): MemberGuard {
  if (!me || (me.role !== 'owner' && me.role !== 'admin')) return { canManage: false, reason: 'Эрх хүрэхгүй' }
  if (member.id === me.id) return { canManage: false, reason: 'Өөрийн эрхийг өөрчлөх боломжгүй' }
  if (member.role === 'owner' && member.status === 'active' && activeOwners <= 1) return { canManage: false, reason: 'Сүүлийн эзэмшигч' }
  if (member.role === 'owner' && me.role !== 'owner') return { canManage: false, reason: 'Эзэмшигчийг зөвхөн эзэмшигч өөрчилнө' }
  return { canManage: true }
}

export function isExpired(iso: string, now: number = Date.now()): boolean {
  return new Date(iso).getTime() < now
}

export const API_SCOPES: { value: string; label: string; hint: string }[] = [
  { value: 'calls:read', label: 'Дуудлага унших', hint: 'Дуудлагын түүх, транскрипт' },
  { value: 'calls:write', label: 'Дуудлага хийх', hint: 'Залгах, таслах, шилжүүлэх' },
  { value: 'campaigns:write', label: 'Кампанит ажил', hint: 'Үүсгэх, эхлүүлэх, зогсоох' },
  { value: 'contacts:write', label: 'Харилцагч', hint: 'Нэмэх, засах, импортлох' },
  { value: 'knowledge:write', label: 'Мэдлэгийн сан', hint: 'Баримт нэмэх, устгах' },
  { value: '*', label: 'Бүрэн эрх', hint: 'Бүх API (админ түвшин)' },
]
export const scopeLabel = (s: string): string => API_SCOPES.find((x) => x.value === s)?.label ?? s

/** Visible part of a key: "cg_live_<prefix>…". */
export function maskedKey(prefix: string): string {
  return `${prefix.startsWith('cg_') ? prefix : `cg_live_${prefix}`}…`
}

export const AUDIT_PAGE_SIZE = 50

/** `yyyy-mm-dd` (local day) → RFC-3339 instant at the start / end of that day. */
export function dayStartISO(day: string): string | undefined {
  return day ? new Date(`${day}T00:00:00`).toISOString() : undefined
}
export function dayEndISO(day: string): string | undefined {
  return day ? new Date(`${day}T23:59:59.999`).toISOString() : undefined
}

export const TIMEZONES: { value: string; label: string }[] = [
  { value: 'Asia/Ulaanbaatar', label: 'Улаанбаатар (UTC+8)' },
  { value: 'Asia/Hovd', label: 'Ховд (UTC+7)' },
  { value: 'Asia/Choibalsan', label: 'Чойбалсан (UTC+8)' },
  { value: 'Asia/Shanghai', label: 'Бээжин (UTC+8)' },
  { value: 'Asia/Seoul', label: 'Сөүл (UTC+9)' },
  { value: 'Asia/Tokyo', label: 'Токио (UTC+9)' },
  { value: 'Europe/Moscow', label: 'Москва (UTC+3)' },
  { value: 'UTC', label: 'UTC' },
]
