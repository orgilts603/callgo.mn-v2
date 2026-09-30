import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { callsApi, type TurnPatchBody, type TurnPatchResponse } from './api'
import { replaceCachedTurn } from './useCallCacheSync'

interface Vars { turnId: string; body: TurnPatchBody }

/** PATCH /api/turns/{id}; updates the cached call detail and lexicon lists on success. */
export function useTurnPatch(callId: string, opts: { successMessage: string; onSuccess?: (r: TurnPatchResponse) => void }) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ turnId, body }: Vars) => callsApi.patchTurn(turnId, body),
    onSuccess: (res) => {
      replaceCachedTurn(qc, callId, res.turn)
      if (res.correction) void qc.invalidateQueries({ queryKey: ['lexicon'] })
      toast.success(opts.successMessage)
      opts.onSuccess?.(res)
    },
    onError: (err: Error) => { toast.error(err.message || 'Хадгалж чадсангүй') },
  })
}
