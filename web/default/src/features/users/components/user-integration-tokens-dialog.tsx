/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useRef } from 'react'
import { z } from 'zod'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { nanoid } from 'nanoid'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { getCurrencyLabel, formatQuotaWithCurrency } from '@/lib/currency'
import { parseQuotaFromDollars } from '@/lib/format'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  addAdminIntegrationTokenQuota,
  getAdminIntegrationTokens,
  type AdminIntegrationToken,
} from '../api'

function formatTokenQuota(quota: number) {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 6,
    digitsSmall: 6,
    abbreviate: false,
  })
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  userId: number
}

export function UserIntegrationTokensDialog(props: Props) {
  const { t } = useTranslation()
  const requestKeys = useRef(new Map<string, string>())
  const requestKey = (tokenId: number, quota: number) =>
    `${props.userId}:${tokenId}:${quota}`
  const getRequestKey = (tokenId: number, quota: number) => {
    const key = requestKey(tokenId, quota)
    let value = requestKeys.current.get(key)
    if (!value) {
      value = nanoid()
      requestKeys.current.set(key, value)
    }
    return value
  }
  const query = useQuery({
    queryKey: ['admin-integration-tokens', props.userId],
    queryFn: () => getAdminIntegrationTokens(props.userId),
    enabled: props.open,
    staleTime: 0,
  })
  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className='max-h-[85vh] overflow-y-auto'>
        <DialogHeader>
          <DialogTitle>
            {t('Integration token quota')} · #{props.userId}
          </DialogTitle>
          <DialogDescription>
            {t(
              'Add quota only to the selected token. The user balance and used quota will not change.'
            )}
          </DialogDescription>
        </DialogHeader>
        {query.isPending && <p role='status'>{t('Loading...')}</p>}
        {query.isError && (
          <div role='alert' className='flex flex-col gap-2'>
            <p>{t('Failed to load integration tokens')}</p>
            <Button variant='outline' onClick={() => void query.refetch()}>
              {t('Retry')}
            </Button>
          </div>
        )}
        {query.data && (
          <div className='flex flex-col gap-4'>
            <p>
              {t('User balance')}: {formatTokenQuota(query.data.user_quota)}
            </p>
            {query.data.tokens.length === 0 && (
              <p>{t('No linked integration tokens')}</p>
            )}
            {query.data.tokens.map((token) => (
              <TokenQuotaForm
                key={`${props.userId}:${token.token_id}`}
                userId={props.userId}
                token={token}
                getRequestKey={getRequestKey}
                clearRequestKey={(quota) =>
                  requestKeys.current.delete(requestKey(token.token_id, quota))
                }
              />
            ))}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function TokenQuotaForm(props: {
  userId: number
  token: AdminIntegrationToken
  getRequestKey: (tokenId: number, quota: number) => string
  clearRequestKey: (quota: number) => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const schema = z.object({
    amount: z.string().refine((value) => {
      const amount = Number(value)
      return (
        Number.isFinite(amount) &&
        amount > 0 &&
        Number.isSafeInteger(parseQuotaFromDollars(amount)) &&
        parseQuotaFromDollars(amount) > 0
      )
    }, t('Enter a positive amount')),
  })
  const form = useForm({
    resolver: zodResolver(schema),
    defaultValues: { amount: '' },
  })
  const mutation = useMutation({
    mutationFn: (payload: { quota: number; idempotencyKey: string }) =>
      addAdminIntegrationTokenQuota({
        ...payload,
        userId: props.userId,
        tokenId: props.token.token_id,
      }),
    onSuccess: (_data, variables) => {
      props.clearRequestKey(variables.quota)
      form.reset()
      toast.success(t('Token quota added successfully'))
      void client.invalidateQueries({
        queryKey: ['admin-integration-tokens', props.userId],
      })
    },
    onError: () => {
      toast.error(
        t(
          'Failed to add token quota. Retry with the same amount to avoid duplicate credit.'
        )
      )
    },
    retry: false,
  })
  const submit = form.handleSubmit((values) => {
    if (mutation.isPending) return
    const quota = parseQuotaFromDollars(Number(values.amount))
    mutation.mutate({
      quota,
      idempotencyKey: props.getRequestKey(props.token.token_id, quota),
    })
  })
  const expired =
    props.token.expired_time !== -1 &&
    props.token.expired_time <= Date.now() / 1000
  let status = t('Unknown')
  if (props.token.status === 1) status = t('Enabled')
  if (props.token.status === 2) status = t('Disabled')
  if (props.token.status === 3 || expired) status = t('Expired')
  if (props.token.status === 4 && !expired) status = t('Exhausted')
  const amountId = `token-quota-${props.token.token_id}`
  const quota = parseQuotaFromDollars(Number(form.watch('amount')))
  return (
    <form
      onSubmit={submit}
      className='flex flex-col gap-3 rounded-lg border p-4'
    >
      <p className='font-medium break-words'>
        {props.token.name} · #{props.token.token_id}
      </p>
      <p className='text-muted-foreground text-sm'>
        {props.token.integration_id} · {status}
      </p>
      <p>
        {t('Token remaining quota')}:{' '}
        {props.token.unlimited_quota
          ? t('Unlimited')
          : formatTokenQuota(props.token.remain_quota)}
      </p>
      {!props.token.unlimited_quota && (
        <FieldGroup>
          <Field data-invalid={!!form.formState.errors.amount}>
            <FieldLabel htmlFor={amountId}>
              {t('Amount to add')} ({getCurrencyLabel()})
            </FieldLabel>
            <Input
              id={amountId}
              type='number'
              step='any'
              min='0'
              disabled={mutation.isPending}
              aria-invalid={!!form.formState.errors.amount}
              {...form.register('amount')}
            />
            {form.formState.errors.amount && (
              <p role='alert' className='text-destructive text-sm'>
                {form.formState.errors.amount.message}
              </p>
            )}
          </Field>
          {quota > 0 && (
            <p className='text-sm'>
              {formatTokenQuota(props.token.remain_quota)} +{' '}
              {formatTokenQuota(quota)} ={' '}
              {formatTokenQuota(props.token.remain_quota + quota)}
            </p>
          )}
          <Button type='submit' disabled={mutation.isPending}>
            {mutation.isPending
              ? t('Saving...')
              : t('Confirm token quota increase')}
          </Button>
        </FieldGroup>
      )}
    </form>
  )
}
