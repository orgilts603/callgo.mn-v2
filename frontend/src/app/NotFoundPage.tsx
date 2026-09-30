import { Link } from 'react-router-dom'
import { Compass } from 'lucide-react'
import { EmptyState } from '@/components/ui'

export default function NotFoundPage() {
  return (
    <div className="flex min-h-[60vh] items-center justify-center">
      <EmptyState
        icon={<Compass />}
        title="Хуудас олдсонгүй"
        description="Таны хайсан хуудас байхгүй эсвэл зөөгдсөн байна. Хаягаа шалгаад дахин оролдоно уу."
        action={
          <Link to="/" className="inline-flex h-8 items-center rounded-[var(--radius-sm)] bg-[var(--accent)] px-3 text-[13px] font-medium text-[var(--fg-on-accent)] hover:bg-[var(--accent-hover)]">
            Хяналтын самбар руу буцах
          </Link>
        }
      />
    </div>
  )
}
