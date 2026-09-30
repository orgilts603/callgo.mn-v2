import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/** Centered, max-width content column used for every page inside the shell. */
export function PageContainer({ className, fluid, ...rest }: HTMLAttributes<HTMLDivElement> & { fluid?: boolean }) {
  return (
    <div
      className={cn(fluid ? 'h-full w-full' : 'mx-auto w-full max-w-[var(--content-max)] px-6 py-6 xl:px-8', className)}
      {...rest}
    />
  )
}
