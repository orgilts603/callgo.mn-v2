import { Component, type ErrorInfo, type ReactNode } from 'react'
import { isRouteErrorResponse, Link, useRouteError } from 'react-router-dom'
import { AlertOctagon, RotateCw } from 'lucide-react'
import { Button } from '@/components/ui'

export function ErrorCard({ title, message, onRetry }: { title?: string; message?: string; onRetry?: () => void }) {
  return (
    <div className="flex min-h-[50vh] items-center justify-center p-6">
      <div className="w-full max-w-md rounded-[var(--radius-lg)] border border-[var(--border)] bg-[var(--surface-1)] p-6 text-center shadow-[var(--shadow-md)]" role="alert">
        <div className="mx-auto mb-4 flex h-10 w-10 items-center justify-center rounded-[var(--radius)] border border-[var(--danger-border)] bg-[var(--danger-soft)] text-[var(--danger)]">
          <AlertOctagon className="h-5 w-5" />
        </div>
        <h2 className="text-sm font-semibold text-[var(--fg)]">{title ?? 'Уучлаарай, алдаа гарлаа'}</h2>
        <p className="mt-1.5 text-xs leading-relaxed text-[var(--fg-muted)]">
          Энэ хэсгийг харуулах үед гэнэтийн алдаа гарлаа. Хуудсыг дахин ачаалж үзнэ үү. Асуудал давтагдвал системийн админд хандана уу.
        </p>
        {message && (
          <pre className="mono mt-4 max-h-32 overflow-auto rounded-[var(--radius-sm)] border border-[var(--border-subtle)] bg-[var(--surface-inset)] px-3 py-2 text-left text-[11px] text-[var(--fg-subtle)] whitespace-pre-wrap">{message}</pre>
        )}
        <div className="mt-5 flex justify-center gap-2">
          <Button variant="secondary" size="sm" onClick={onRetry ?? (() => window.location.reload())}>
            <RotateCw /> Дахин ачаалах
          </Button>
          <Link to="/" className="inline-flex h-7 items-center rounded-[var(--radius-sm)] px-2.5 text-xs font-medium text-[var(--fg-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--fg)]">
            Нүүр хуудас
          </Link>
        </div>
      </div>
    </div>
  )
}

interface State { error: Error | null }

/** Catches render errors below it. Give it a `key` (e.g. pathname) to reset on navigation. */
export class ErrorBoundary extends Component<{ children: ReactNode; fallback?: ReactNode }, State> {
  state: State = { error: null }
  static getDerivedStateFromError(error: Error): State { return { error } }
  componentDidCatch(error: Error, info: ErrorInfo) { console.error('[ErrorBoundary]', error, info.componentStack) }
  render() {
    if (this.state.error) return this.props.fallback ?? <ErrorCard message={this.state.error.message} onRetry={() => window.location.reload()} />
    return this.props.children
  }
}

/** `errorElement` for the data router (loader/action/render errors). */
export function RouteErrorBoundary() {
  const err = useRouteError()
  if (isRouteErrorResponse(err)) {
    return <ErrorCard title={err.status === 404 ? 'Хуудас олдсонгүй' : `Алдаа ${err.status}`} message={err.statusText} />
  }
  return <ErrorCard message={err instanceof Error ? err.message : String(err)} />
}
