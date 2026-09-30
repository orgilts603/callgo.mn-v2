import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { CallbackRequest, CallbackStatus } from '@/lib/types'

export const callbacksKey = ['callbacks'] as const
export const CALLBACKS_PAGE_SIZE = 25

export interface CallbacksParams { status: CallbackStatus | ''; offset: number }
export interface CallbackCreateBody {
  phone: string; name?: string; note?: string; dueAt: string; sipNumberId?: string; agentProfileId?: string
}
export interface CallbackUpdateBody { dueAt?: string; note?: string; status?: 'canceled' }

export function useCallbacks({ status, offset }: CallbacksParams) {
  return useQuery({
    queryKey: [...callbacksKey, status, offset] as const,
    queryFn: () => api.get<{ items: CallbackRequest[]; total: number }>('/callbacks', { status, limit: CALLBACKS_PAGE_SIZE, offset }),
    placeholderData: (prev) => prev,
  })
}
export function useCreateCallback() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (body: CallbackCreateBody) => api.post<{ callback?: CallbackRequest }>('/callbacks', body),
    onSuccess: () => qc.invalidateQueries({ queryKey: callbacksKey }),
  })
}
export function useUpdateCallback() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: string; body: CallbackUpdateBody }) => api.put<{ callback?: CallbackRequest }>(`/callbacks/${id}`, body),
    onSuccess: () => qc.invalidateQueries({ queryKey: callbacksKey }),
  })
}
