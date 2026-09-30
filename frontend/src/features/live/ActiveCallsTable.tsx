import { createContext, memo, useContext, useMemo } from 'react'
import {
  FlexRender, createColumnHelper, createSortedRowModel, rowSortingFeature, sortFn_basic, sortFn_text, tableFeatures, useTable,
  type Row,
} from '@tanstack/react-table'
import { ArrowDown, ArrowUp, ArrowUpDown, Bot, Headset, PanelRightOpen, PhoneOff, User } from 'lucide-react'
import { AgentStateBadge, Button, CallStatusBadge, TBody, TD, TH, THead, TR, Table } from '@/components/ui'
import { cn, fmtAgo, fmtDateTime, fmtPhone } from '@/lib/utils'
import type { CallStatus, Speaker } from '@/lib/types'
import { CallDuration, DIRECTION_LABEL, DirectionIcon } from '@/features/calls/callFormat'
import type { CallRow } from './types'
import { useTicker } from './useTicker'

interface RowActions {
  onOpen: (id: string) => void
  onHangup: (row: CallRow) => void
}
const ActionsContext = createContext<RowActions>({ onOpen: () => {}, onHangup: () => {} })

const STATUS_ORDER: Record<CallStatus, number> = {
  active: 0, ringing: 1, queued: 2, completed: 3, failed: 4, no_answer: 5, busy: 6, voicemail: 7,
}
const SPEAKER_ICON: Record<Speaker, typeof User> = { customer: User, agent: Bot, human: Headset }

function Ago({ iso }: { iso: string }) {
  useTicker(1000)
  return <span className="whitespace-nowrap tabular-nums text-[var(--fg-muted)]" title={fmtDateTime(iso)}>{fmtAgo(iso)}</span>
}

function ActionsCell({ row }: { row: CallRow }) {
  const { onOpen, onHangup } = useContext(ActionsContext)
  const canHangup = row.call.status === 'active' || row.call.status === 'ringing'
  return (
    <div className="flex items-center justify-end gap-1" onClick={(e) => e.stopPropagation()}>
      <Button size="icon" variant="ghost" className="h-7 w-7" aria-label="Дэлгэрэнгүй" title="Дэлгэрэнгүй" onClick={() => onOpen(row.id)}>
        <PanelRightOpen className="h-4 w-4" />
      </Button>
      <Button size="icon" variant="ghost" className="h-7 w-7 text-red-300 hover:text-red-200" aria-label="Таслах" title="Дуудлага таслах"
        disabled={!canHangup} onClick={() => onHangup(row)}>
        <PhoneOff className="h-4 w-4" />
      </Button>
    </div>
  )
}

const features = tableFeatures({ rowSortingFeature, sortedRowModel: createSortedRowModel() })
const helper = createColumnHelper<typeof features, CallRow>()

export const columns = helper.columns([
  helper.accessor((r) => STATUS_ORDER[r.call.status], {
    id: 'status', header: 'Төлөв', sortFn: sortFn_basic,
    cell: ({ row }) => <CallStatusBadge status={row.original.call.status} />,
  }),
  helper.accessor((r) => r.call.direction, {
    id: 'direction', header: 'Чиглэл', sortFn: sortFn_text,
    cell: ({ row }) => (
      <span className="inline-flex items-center gap-1.5 text-xs text-[var(--fg-muted)]">
        <DirectionIcon direction={row.original.call.direction} className="h-3.5 w-3.5" />{DIRECTION_LABEL[row.original.call.direction]}
      </span>
    ),
  }),
  helper.accessor((r) => r.call.fromNumber, {
    id: 'numbers', header: 'Хаанаас → Хаашаа', sortFn: sortFn_text,
    cell: ({ row }) => (
      <span className="whitespace-nowrap font-mono text-xs">
        {fmtPhone(row.original.call.fromNumber)} <span className="text-[var(--fg-subtle)]">→</span> {fmtPhone(row.original.call.toNumber)}
      </span>
    ),
  }),
  helper.accessor((r) => r.contactName ?? '', {
    id: 'contact', header: 'Харилцагч', sortFn: sortFn_text,
    cell: ({ row }) => row.original.contactName || <span className="text-[var(--fg-subtle)]">—</span>,
  }),
  helper.accessor((r) => r.agentState ?? '', {
    id: 'agent', header: 'Агент', sortFn: sortFn_text,
    cell: ({ row }) => row.original.agentState ? <AgentStateBadge state={row.original.agentState} /> : <span className="text-[var(--fg-subtle)]">—</span>,
  }),
  helper.accessor((r) => r.campaignName ?? '', {
    id: 'campaign', header: 'Кампанит ажил', sortFn: sortFn_text,
    cell: ({ row }) => row.original.campaignName
      ? <span className="block max-w-40 truncate text-xs">{row.original.campaignName}</span>
      : <span className="text-[var(--fg-subtle)]">—</span>,
  }),
  helper.accessor((r) => Date.parse(r.call.startedAt) || 0, {
    id: 'started', header: 'Эхэлсэн', sortFn: sortFn_basic, sortDescFirst: true,
    cell: ({ row }) => <Ago iso={row.original.call.startedAt} />,
  }),
  // Longer duration == earlier start, hence inverted.
  helper.accessor((r) => Date.parse(r.call.answeredAt || r.call.startedAt) || 0, {
    id: 'duration', header: 'Хугацаа', sortFn: sortFn_basic, invertSorting: true,
    cell: ({ row }) => <CallDuration call={row.original.call} className="text-xs" />,
  }),
  helper.display({
    id: 'snippet', header: 'Сүүлийн яриа',
    cell: ({ row }) => {
      const s = row.original.snippet
      if (!s) return <span className="text-[var(--fg-subtle)]">—</span>
      const Icon = SPEAKER_ICON[s.speaker] ?? User
      return (
        <span className={cn('flex max-w-72 items-center gap-1.5 text-xs', s.partial ? 'italic text-[var(--fg-subtle)]' : 'text-[var(--fg-muted)]')} title={s.text}>
          <Icon className="h-3 w-3 shrink-0" /><span className="truncate">{s.text}</span>
        </span>
      )
    },
  }),
  helper.display({ id: 'actions', header: '', cell: ({ row }) => <ActionsCell row={row.original} /> }),
])

type LiveRow = Row<typeof features, CallRow>

interface RowViewProps { row: LiveRow; data: CallRow; selected: boolean; onOpen: (id: string) => void }

// `data` is the reused CallRow object: unchanged rows skip re-rendering entirely
// (ticking cells update themselves through the shared ticker).
const RowView = memo(function RowView({ row, data, selected, onOpen }: RowViewProps) {
  return (
    <TR onClick={() => onOpen(data.id)} data-testid="live-row" data-call-id={data.id}
      className={cn('cursor-pointer', selected && 'bg-[var(--accent)]/10 hover:bg-[var(--accent)]/15')}>
      {row.getAllCells().map((cell) => (
        <TD key={cell.id} className={cell.column.id === 'actions' ? 'w-20' : undefined}><FlexRender cell={cell} /></TD>
      ))}
    </TR>
  )
}, (a, b) => a.data === b.data && a.selected === b.selected && a.onOpen === b.onOpen)

export interface ActiveCallsTableProps {
  rows: CallRow[]
  selectedId?: string | null
  onOpen: (id: string) => void
  onHangup: (row: CallRow) => void
}

/** Sortable table of live calls (TanStack Table v9). `rows` must be referentially stable. */
export function ActiveCallsTable({ rows, selectedId, onOpen, onHangup }: ActiveCallsTableProps) {
  const table = useTable({
    features,
    columns,
    data: rows,
    getRowId: (r) => r.id,
    initialState: { sorting: [{ id: 'started', desc: true }] },
  })

  const actions = useMemo<RowActions>(() => ({ onOpen, onHangup }), [onOpen, onHangup])

  return (
    <ActionsContext.Provider value={actions}>
      <Table>
        <THead>
          {table.getHeaderGroups().map((group) => (
            <TR key={group.id} className="hover:bg-transparent">
              {group.headers.map((header) => {
                const canSort = header.column.getCanSort()
                const sorted = header.column.getIsSorted()
                const SortIcon = sorted === 'asc' ? ArrowUp : sorted === 'desc' ? ArrowDown : ArrowUpDown
                return (
                  <TH key={header.id} aria-sort={sorted === 'asc' ? 'ascending' : sorted === 'desc' ? 'descending' : undefined}>
                    {header.isPlaceholder ? null : canSort ? (
                      <button type="button" onClick={header.column.getToggleSortingHandler()}
                        className="inline-flex items-center gap-1 whitespace-nowrap uppercase hover:text-[var(--fg)]">
                        <table.FlexRender header={header} />
                        <SortIcon className={cn('h-3 w-3', !sorted && 'opacity-40')} />
                      </button>
                    ) : <table.FlexRender header={header} />}
                  </TH>
                )
              })}
            </TR>
          ))}
        </THead>
        <TBody>
          {table.getRowModel().rows.map((row) => (
            <RowView key={row.id} row={row} data={row.original} selected={row.id === selectedId} onOpen={onOpen} />
          ))}
        </TBody>
      </Table>
    </ActionsContext.Provider>
  )
}
