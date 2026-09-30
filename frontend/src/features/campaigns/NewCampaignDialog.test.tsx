import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { AgentProfile, Campaign, CampaignPreview, SIPNumber } from '@/lib/types'
import { buildCampaignFormData, defaultValues, NewCampaignDialog } from './NewCampaignDialog'

const sips = [
  { id: 'sip-1', label: 'Үндсэн', number: '+97677001122', allowOutbound: true, active: true, agentProfileId: 'ap-1' },
  { id: 'sip-2', label: 'Inbound only', number: '+97677003344', allowOutbound: false, active: true },
  { id: 'sip-3', label: 'Disabled', number: '+97677005566', allowOutbound: true, active: false },
] as unknown as SIPNumber[]
const profiles = [{ id: 'ap-1', name: 'Борлуулагч' }, { id: 'ap-2', name: 'Сануулагч' }] as unknown as AgentProfile[]
const csvPreview: CampaignPreview = {
  columns: ['Утас', 'Нэр', 'x'], rows: [['99112233', 'Бат', '1'], ['88112233', 'Сараа', '2']], total: 2, format: 'csv',
  mapping: { Утас: 'phone', Нэр: 'name', x: 'var' },
}
const xlsxPreview: CampaignPreview = { ...csvPreview, format: 'xlsx', total: 120 }
const created = { id: 'c-new', name: 'Тест', total: 2, completed: 0, failed: 0 } as unknown as Campaign

function renderDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter><NewCampaignDialog open onClose={() => {}} /></MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('NewCampaignDialog', () => {
  beforeEach(() => {
    vi.spyOn(api, 'get').mockImplementation(async (path: string) => {
      if (path === '/sip-numbers') return { items: sips } as never
      if (path === '/agent-profiles') return { items: profiles } as never
      return { items: [] } as never
    })
  })
  afterEach(() => { cleanup(); vi.restoreAllMocks() })

  it('only offers outbound-enabled active SIP numbers', async () => {
    renderDialog()
    await screen.findByRole('option', { name: /Үндсэн/ })
    expect(screen.queryByRole('option', { name: /Inbound only/ })).toBeNull()
    expect(screen.queryByRole('option', { name: /Disabled/ })).toBeNull()
  })

  /** Fill step 1 and advance to the schedule step. */
  async function fillStep1() {
    await screen.findByRole('option', { name: /Үндсэн/ })
    fireEvent.change(screen.getByPlaceholderText(/Долдугаар/), { target: { value: 'Тест' } })
    fireEvent.change(screen.getByPlaceholderText(/Сайн байна уу/), { target: { value: 'Сайн уу {{name}}' } })
    fireEvent.change(screen.getAllByRole('combobox')[0], { target: { value: 'sip-1' } })
    fireEvent.change(screen.getAllByRole('combobox')[1], { target: { value: 'ap-2' } })
    fireEvent.change(screen.getByLabelText(/Зэрэг дуудлага \(1-50\)/), { target: { value: '7' } })
    fireEvent.change(screen.getByLabelText(/Дахин оролдох/), { target: { value: '3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Үргэлжлүүлэх' }))
  }
  const next = () => fireEvent.click(screen.getByRole('button', { name: 'Үргэлжлүүлэх' }))
  const upload = (f: File) => fireEvent.change(screen.getByLabelText('CSV эсвэл Excel файл'), { target: { files: [f] } })

  function mockPost(preview: CampaignPreview = csvPreview) {
    return vi.spyOn(api, 'post').mockImplementation(async (path: string) => {
      if (path === '/campaigns/preview') return preview as never
      return { campaign: created, targets: { imported: 1, skipped: 1, errors: [{ row: 3, message: 'bad phone' }] } } as never
    })
  }
  const createCall = (post: ReturnType<typeof mockPost>) => post.mock.calls.find((c) => c[0] === '/campaigns')!

  it('walks the steps, previews via the backend and posts multipart FormData with v2 fields', async () => {
    const post = mockPost()
    renderDialog()
    await fillStep1()

    // Step 2: schedule. Default is 24/7; turn it off and edit the window.
    expect(screen.getByTestId('schedule-editor')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('switch', { name: '24/7' }))
    fireEvent.click(screen.getByRole('button', { name: 'Бя' }))   // add Saturday
    fireEvent.click(screen.getByRole('button', { name: 'Мя' }))   // remove Tuesday
    fireEvent.change(screen.getByLabelText('Эхлэх цаг'), { target: { value: '10:00' } })
    fireEvent.change(screen.getByLabelText('Дуусах цаг'), { target: { value: '17:30' } })
    fireEvent.change(screen.getByLabelText(/Минутад залгах/), { target: { value: '12' } })
    fireEvent.change(screen.getByLabelText('Цагийн бүс'), { target: { value: 'UTC' } })
    next()

    // Step 3: outcomes. Drop "no_contact" (last row).
    expect(screen.getAllByTestId('outcome-row')).toHaveLength(5)
    fireEvent.click(screen.getByRole('button', { name: 'Ангилал 5 устгах' }))
    next()

    // Step 4: file + dry run
    expect(screen.getByText('CSV эсвэл Excel (.xlsx)')).toBeInTheDocument()
    upload(new File(['x'], 'list.csv', { type: 'text/csv' }))
    expect(await screen.findByTestId('phone-detected')).toHaveTextContent('Утас')
    expect(screen.getByTestId('csv-preview')).toHaveTextContent('2 мөр')
    fireEvent.change(screen.getByLabelText(/Туршилт/), { target: { value: '5' } })
    expect(screen.getByText(/Эхний 5 дугаарт залгаад автоматаар түр зогсоно/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Үүсгэх' }))
    await waitFor(() => expect(createCall(post)).toBeDefined())
    const [path, body] = createCall(post)
    expect(path).toBe('/campaigns')
    const fd = body as FormData
    expect(fd).toBeInstanceOf(FormData)
    expect(fd.get('name')).toBe('Тест')
    expect(fd.get('script')).toBe('Сайн уу {{name}}')
    expect(fd.get('sipNumberId')).toBe('sip-1')
    expect(fd.get('agentProfileId')).toBe('ap-2')
    expect(fd.get('concurrency')).toBe('7')
    expect(fd.get('maxAttempts')).toBe('3')
    expect(fd.get('dryRunLimit')).toBe('5')
    expect((fd.get('file') as File).name).toBe('list.csv')
    expect(JSON.parse(fd.get('schedule') as string)).toEqual({
      timezone: 'UTC', weekdays: [1, 3, 4, 5, 6], startTime: '10:00', endTime: '17:30', pacePerMinute: 12,
    })
    const outcomes = JSON.parse(fd.get('outcomes') as string) as { code: string; terminal: boolean }[]
    expect(outcomes.map((o) => o.code)).toEqual(['agreed', 'declined', 'callback', 'wrong_number'])
    expect(outcomes.find((o) => o.code === 'callback')?.terminal).toBe(false)

    const result = await screen.findByTestId('result')
    expect(result).toHaveTextContent('Импортолсон')
    fireEvent.click(screen.getByText(/Алдааны жагсаалт/))
    expect(screen.getByText(/bad phone/)).toBeInTheDocument()
  })

  it('defaults to 24/7 (empty schedule), default outcomes and dryRunLimit 0', async () => {
    const post = mockPost()
    renderDialog()
    await fillStep1(); next(); next()
    upload(new File(['x'], 'list.csv'))
    await screen.findByTestId('phone-detected')
    fireEvent.click(screen.getByRole('button', { name: 'Үүсгэх' }))
    await waitFor(() => expect(createCall(post)).toBeDefined())
    const fd = createCall(post)[1] as FormData
    expect(JSON.parse(fd.get('schedule') as string)).toEqual({})
    expect((JSON.parse(fd.get('outcomes') as string) as unknown[]).length).toBe(5)
    expect(fd.get('dryRunLimit')).toBe('0')
  })

  it('sends [] outcomes when "Ангилалгүй" is on', async () => {
    const post = mockPost()
    renderDialog()
    await fillStep1(); next()
    fireEvent.click(screen.getByRole('switch', { name: 'Ангилалгүй' }))
    expect(screen.queryByTestId('outcome-row')).toBeNull()
    next()
    upload(new File(['x'], 'list.csv'))
    await screen.findByTestId('phone-detected')
    fireEvent.click(screen.getByRole('button', { name: 'Үүсгэх' }))
    await waitFor(() => expect(createCall(post)).toBeDefined())
    expect((createCall(post)[1] as FormData).get('outcomes')).toBe('[]')
  })

  it('validates the outcomes editor: unique non-empty codes and labels block the next step', async () => {
    mockPost()
    renderDialog()
    await fillStep1(); next()
    const nextBtn = screen.getByRole('button', { name: 'Үргэлжлүүлэх' })
    expect(nextBtn).toBeEnabled()

    fireEvent.change(screen.getByLabelText('Код 2'), { target: { value: 'agreed' } })
    expect(screen.getAllByText('Код давхардсан')).toHaveLength(2)
    expect(nextBtn).toBeDisabled()

    fireEvent.change(screen.getByLabelText('Код 2'), { target: { value: '' } })
    expect(screen.getByText('Код оруулна уу')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Код 2'), { target: { value: 'declined' } })
    fireEvent.change(screen.getByLabelText('Нэр 1'), { target: { value: ' ' } })
    expect(screen.getByText('Нэр оруулна уу')).toBeInTheDocument()
    expect(nextBtn).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Нэр 1'), { target: { value: 'Зөвшөөрсөн' } })
    expect(nextBtn).toBeEnabled()

    fireEvent.click(screen.getByRole('button', { name: 'Ангилал нэмэх' }))
    expect(screen.getAllByTestId('outcome-row')).toHaveLength(6)
    expect(nextBtn).toBeDisabled() // new row is empty
  })

  it('blocks the schedule step with no weekday selected', async () => {
    mockPost()
    renderDialog()
    await fillStep1()
    fireEvent.click(screen.getByRole('switch', { name: '24/7' }))
    for (const d of ['Да', 'Мя', 'Лх', 'Пү', 'Ба']) fireEvent.click(screen.getByRole('button', { name: d }))
    expect(screen.getByText('Дор хаяж нэг өдөр сонгоно уу')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Үргэлжлүүлэх' })).toBeDisabled()
  })

  it('previews Excel through POST /campaigns/preview and shows the detected mapping', async () => {
    const post = mockPost(xlsxPreview)
    renderDialog()
    await fillStep1(); next(); next()
    const xlsx = new File(['PK'], 'list.xlsx', { type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet' })
    expect(screen.getByLabelText('CSV эсвэл Excel файл')).toHaveAttribute('accept', expect.stringContaining('.xlsx'))
    upload(xlsx)
    const preview = await screen.findByTestId('csv-preview')
    const [path, body] = post.mock.calls[0]
    expect(path).toBe('/campaigns/preview')
    expect((body as FormData).get('file')).toBe(xlsx)
    expect(screen.getByTestId('preview-format')).toHaveTextContent('Excel')
    expect(preview).toHaveTextContent('120 мөр')
    expect(screen.getByTestId('map-phone')).toHaveTextContent('Утас')
    expect(screen.getByTestId('map-name')).toHaveTextContent('Нэр')
    expect(screen.getByTestId('map-var')).toHaveTextContent('Хувьсагч')
    expect(preview).toHaveTextContent('Эхний 2 мөрийг харуулав')
    expect(screen.getByRole('button', { name: 'Үүсгэх' })).toBeEnabled()
  })

  it('blocks submit when the backend finds no phone column', async () => {
    vi.spyOn(api, 'post').mockResolvedValue({ columns: ['a', 'b'], rows: [['1', '2']], mapping: { a: 'var', b: 'var' }, total: 1, format: 'csv' } as never)
    renderDialog()
    await fillStep1(); next(); next()
    upload(new File(['a,b\n1,2\n'], 'x.csv'))
    await screen.findByTestId('phone-missing')
    expect(screen.getByRole('button', { name: 'Үүсгэх' })).toBeDisabled()
  })

  it('buildCampaignFormData serialises schedule, outcomes and dryRunLimit', () => {
    const fd = buildCampaignFormData({ ...defaultValues, name: ' A ', dryRunLimit: 3 }, new File(['x'], 'a.csv'))
    expect(fd.get('name')).toBe('A')
    expect(fd.get('schedule')).toBe('{}')
    expect(fd.get('dryRunLimit')).toBe('3')
    expect(JSON.parse(fd.get('outcomes') as string)[0]).toMatchObject({ code: 'agreed', label: 'Зөвшөөрсөн', terminal: true })
  })
})
