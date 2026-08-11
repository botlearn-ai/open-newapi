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
import { useEffect, useMemo, useRef, useState } from 'react'
import * as z from 'zod'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { CheckCircle2, Loader2, XCircle } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import { testLarkNotify } from '../api'
import {
  SettingsForm,
  SettingsSwitchContent,
  SettingsSwitchItem,
} from '../components/settings-form-layout'
import { SettingsPageFormActions } from '../components/settings-page-context'
import { SettingsSection } from '../components/settings-section'
import { useUpdateOption } from '../hooks/use-update-option'
import { safeNumberFieldProps } from '../utils/numeric-field'

// Dotted keys MUST be modelled as a nested object here. react-hook-form treats a
// dotted `name` as a nested path, so a schema with literal flat keys silently
// desyncs from form state and every save becomes a no-op.
const larkSchema = z
  .object({
    lark_notify_setting: z.object({
      enabled: z.boolean(),
      webhook_url: z.string(),
      sign_secret: z.string(),
      alert_on_relay_error: z.boolean(),
      alert_on_channel_test: z.boolean(),
      throttle_seconds: z.coerce
        .number()
        .int()
        .min(0, 'Throttle must be zero or greater'),
      use_card: z.boolean(),
    }),
  })
  .superRefine((values, ctx) => {
    const lark = values.lark_notify_setting
    if (!lark.enabled) return
    const url = lark.webhook_url.trim()
    if (!url) {
      ctx.addIssue({
        code: 'custom',
        path: ['lark_notify_setting', 'webhook_url'],
        message: 'Webhook URL is required when Feishu alerts are enabled',
      })
      return
    }
    if (!/^https:\/\//i.test(url)) {
      ctx.addIssue({
        code: 'custom',
        path: ['lark_notify_setting', 'webhook_url'],
        message: 'Feishu webhook URL must start with https://',
      })
    }
  })

type LarkFormInput = z.input<typeof larkSchema>
type LarkFormValues = z.output<typeof larkSchema>

type LarkNotifySettingsSectionProps = {
  defaultValues: {
    'lark_notify_setting.enabled': boolean
    'lark_notify_setting.webhook_url': string
    'lark_notify_setting.sign_secret': string
    'lark_notify_setting.alert_on_relay_error': boolean
    'lark_notify_setting.alert_on_channel_test': boolean
    'lark_notify_setting.throttle_seconds': number
    'lark_notify_setting.use_card': boolean
  }
}

// sign_secret is DELIBERATELY ABSENT from the baseline. GetOptions strips keys
// ending in "secret", so a refetch can never reproduce the stored value; if it
// participated in the diff loop, the next save would push '' and destroy the
// stored credential. It is submitted separately, only when non-empty.
type NormalizedLarkValues = {
  'lark_notify_setting.enabled': boolean
  'lark_notify_setting.webhook_url': string
  'lark_notify_setting.alert_on_relay_error': boolean
  'lark_notify_setting.alert_on_channel_test': boolean
  'lark_notify_setting.throttle_seconds': number
  'lark_notify_setting.use_card': boolean
}

const buildFormDefaults = (
  d: LarkNotifySettingsSectionProps['defaultValues']
): LarkFormInput => ({
  lark_notify_setting: {
    enabled: d['lark_notify_setting.enabled'],
    webhook_url: d['lark_notify_setting.webhook_url'] ?? '',
    // Always start blank: the API never returns the stored secret.
    sign_secret: '',
    alert_on_relay_error: d['lark_notify_setting.alert_on_relay_error'],
    alert_on_channel_test: d['lark_notify_setting.alert_on_channel_test'],
    throttle_seconds: d['lark_notify_setting.throttle_seconds'],
    use_card: d['lark_notify_setting.use_card'],
  },
})

const normalizeDefaults = (
  d: LarkNotifySettingsSectionProps['defaultValues']
): NormalizedLarkValues => ({
  'lark_notify_setting.enabled': d['lark_notify_setting.enabled'],
  'lark_notify_setting.webhook_url': (
    d['lark_notify_setting.webhook_url'] ?? ''
  ).trim(),
  'lark_notify_setting.alert_on_relay_error':
    d['lark_notify_setting.alert_on_relay_error'],
  'lark_notify_setting.alert_on_channel_test':
    d['lark_notify_setting.alert_on_channel_test'],
  'lark_notify_setting.throttle_seconds':
    d['lark_notify_setting.throttle_seconds'],
  'lark_notify_setting.use_card': d['lark_notify_setting.use_card'],
})

const normalizeFormValues = (v: LarkFormValues): NormalizedLarkValues => ({
  'lark_notify_setting.enabled': v.lark_notify_setting.enabled,
  'lark_notify_setting.webhook_url': v.lark_notify_setting.webhook_url.trim(),
  'lark_notify_setting.alert_on_relay_error':
    v.lark_notify_setting.alert_on_relay_error,
  'lark_notify_setting.alert_on_channel_test':
    v.lark_notify_setting.alert_on_channel_test,
  'lark_notify_setting.throttle_seconds':
    v.lark_notify_setting.throttle_seconds,
  'lark_notify_setting.use_card': v.lark_notify_setting.use_card,
})

export function LarkNotifySettingsSection({
  defaultValues,
}: LarkNotifySettingsSectionProps) {
  const { t } = useTranslation()
  const updateOption = useUpdateOption()

  const formDefaults = useMemo(
    () => buildFormDefaults(defaultValues),
    [defaultValues]
  )

  const form = useForm<LarkFormInput, unknown, LarkFormValues>({
    resolver: zodResolver(larkSchema),
    defaultValues: formDefaults,
  })

  const baselineRef = useRef<NormalizedLarkValues>(
    normalizeDefaults(defaultValues)
  )
  const baselineSerializedRef = useRef(
    JSON.stringify(normalizeDefaults(defaultValues))
  )

  // Resets the form AND the baseline atomically when fresh server values arrive.
  // Used instead of useResetForm, which resets the form without touching the
  // baseline — that mismatch is what makes a stale baseline resubmit old values.
  useEffect(() => {
    const next = normalizeDefaults(defaultValues)
    const serialized = JSON.stringify(next)
    if (serialized === baselineSerializedRef.current) return
    baselineRef.current = next
    baselineSerializedRef.current = serialized
    form.reset(buildFormDefaults(defaultValues))
  }, [defaultValues, form])

  const [testState, setTestState] = useState<{
    loading: boolean
    ok: boolean | null
    error: string | null
  }>({ loading: false, ok: null, error: null })

  const enabled = form.watch('lark_notify_setting.enabled')

  const onSubmit = async (values: LarkFormValues) => {
    const normalized = normalizeFormValues(values)

    const updates: Array<{ key: string; value: string | boolean | number }> = (
      Object.keys(normalized) as Array<keyof NormalizedLarkValues>
    )
      .filter((key) => normalized[key] !== baselineRef.current[key])
      .map((key) => ({ key, value: normalized[key] }))

    // Write-only credential: blank means "keep whatever is stored".
    const signSecret = values.lark_notify_setting.sign_secret.trim()
    if (signSecret) {
      updates.push({
        key: 'lark_notify_setting.sign_secret',
        value: signSecret,
      })
    }

    if (updates.length === 0) {
      toast.info(t('No changes to save'))
      return
    }

    for (const update of updates) {
      await updateOption.mutateAsync(update)
    }

    baselineRef.current = normalized
    baselineSerializedRef.current = JSON.stringify(normalized)
    // Clear the secret box so its placeholder hint reads correctly again.
    form.setValue('lark_notify_setting.sign_secret', '')
  }

  const handleClearSecret = async () => {
    await updateOption.mutateAsync({
      key: 'lark_notify_setting.sign_secret',
      value: '',
    })
    form.setValue('lark_notify_setting.sign_secret', '')
    toast.success(t('Signing secret cleared'))
  }

  const handleSendTest = async () => {
    const webhookUrl = form
      .getValues('lark_notify_setting.webhook_url')
      .trim()
    if (!webhookUrl) {
      form.setError('lark_notify_setting.webhook_url', {
        message: t('Webhook URL is required to send a test alert'),
      })
      return
    }
    setTestState({ loading: true, ok: null, error: null })
    try {
      const res = await testLarkNotify({
        webhook_url: webhookUrl,
        sign_secret: form
          .getValues('lark_notify_setting.sign_secret')
          .trim(),
      })
      setTestState(
        res?.success
          ? { loading: false, ok: true, error: null }
          : {
              loading: false,
              ok: false,
              error: res?.message || t('Failed to send test alert'),
            }
      )
    } catch (err) {
      setTestState({
        loading: false,
        ok: false,
        error:
          err instanceof Error ? err.message : t('Failed to send test alert'),
      })
    }
  }

  return (
    <SettingsSection title={t('Feishu Alerts')}>
      <Form {...form}>
        <SettingsForm
          onSubmit={form.handleSubmit(onSubmit)}
          autoComplete='off'
        >
          <SettingsPageFormActions
            onSave={form.handleSubmit(onSubmit)}
            isSaving={updateOption.isPending}
            saveLabel='Save Feishu settings'
          />

          <FormField
            control={form.control}
            name='lark_notify_setting.enabled'
            render={({ field }) => (
              <SettingsSwitchItem>
                <SettingsSwitchContent>
                  <FormLabel>{t('Enable Feishu alerts')}</FormLabel>
                  <FormDescription>
                    {t(
                      'Send failure alerts to a Feishu group chat via a custom bot webhook'
                    )}
                  </FormDescription>
                </SettingsSwitchContent>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
              </SettingsSwitchItem>
            )}
          />

          {enabled ? (
            <>
              <FormField
                control={form.control}
                name='lark_notify_setting.webhook_url'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Feishu Webhook URL')}</FormLabel>
                    <div className='flex gap-2'>
                      <FormControl>
                        <Input
                          autoComplete='off'
                          placeholder='https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx'
                          {...field}
                          onChange={(event) =>
                            field.onChange(event.target.value)
                          }
                        />
                      </FormControl>
                      <Button
                        type='button'
                        variant='secondary'
                        onClick={handleSendTest}
                        disabled={testState.loading || updateOption.isPending}
                        className='shrink-0'
                      >
                        {testState.loading ? (
                          <Loader2 className='me-2 size-4 animate-spin' />
                        ) : null}
                        {t('Send Test Alert')}
                      </Button>
                    </div>
                    <FormDescription>
                      {t('Webhook address of the Feishu group custom bot')}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <FormField
                control={form.control}
                name='lark_notify_setting.sign_secret'
                render={({ field }) => (
                  <FormItem>
                    <FormLabel>{t('Signing Secret')}</FormLabel>
                    <div className='flex gap-2'>
                      <FormControl>
                        <Input
                          autoComplete='off'
                          type='password'
                          placeholder={t('Enter new secret to update')}
                          {...field}
                          onChange={(event) =>
                            field.onChange(event.target.value)
                          }
                        />
                      </FormControl>
                      <Button
                        type='button'
                        variant='ghost'
                        onClick={handleClearSecret}
                        disabled={updateOption.isPending}
                        className='shrink-0'
                      >
                        {t('Clear signing secret')}
                      </Button>
                    </div>
                    <FormDescription>
                      {t(
                        'Leave blank to keep the existing secret. Only needed when signature verification is enabled on the bot.'
                      )}
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <div className='grid gap-6 md:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='lark_notify_setting.alert_on_relay_error'
                  render={({ field }) => (
                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>{t('Alert on LLM call failures')}</FormLabel>
                        <FormDescription>
                          {t(
                            'Push an alert whenever an upstream LLM request fails'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </SettingsSwitchItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='lark_notify_setting.alert_on_channel_test'
                  render={({ field }) => (
                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>
                          {t('Alert on channel test failures')}
                        </FormLabel>
                        <FormDescription>
                          {t(
                            'Also alert when a scheduled channel test fails badly enough to disable it'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </SettingsSwitchItem>
                  )}
                />
              </div>

              <div className='grid gap-6 md:grid-cols-2'>
                <FormField
                  control={form.control}
                  name='lark_notify_setting.throttle_seconds'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Alert throttle (seconds)')}</FormLabel>
                      <FormControl>
                        <Input
                          type='number'
                          min={0}
                          step={1}
                          {...safeNumberFieldProps(field)}
                        />
                      </FormControl>
                      <FormDescription>
                        {t(
                          'Deduplicate alerts for the same channel, model and status code within this window. Set 0 to alert on every failure.'
                        )}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='lark_notify_setting.use_card'
                  render={({ field }) => (
                    <SettingsSwitchItem>
                      <SettingsSwitchContent>
                        <FormLabel>{t('Use rich card messages')}</FormLabel>
                        <FormDescription>
                          {t(
                            'Send colour-coded cards instead of plain text. Turn off if your bot rejects cards.'
                          )}
                        </FormDescription>
                      </SettingsSwitchContent>
                      <FormControl>
                        <Switch
                          checked={field.value}
                          onCheckedChange={field.onChange}
                        />
                      </FormControl>
                    </SettingsSwitchItem>
                  )}
                />
              </div>

              <Alert variant='default'>
                <AlertTitle>{t('How to create a Feishu group bot')}</AlertTitle>
                <AlertDescription>
                  <ul className='list-disc space-y-1 pl-5'>
                    <li>
                      {t(
                        'Open the target Feishu group, then Settings > Group Bots > Add Bot > Custom Bot'
                      )}
                    </li>
                    <li>
                      {t('Copy the generated webhook URL and paste it here')}
                    </li>
                    <li>
                      {t(
                        'Optionally enable signature verification and paste the secret above'
                      )}
                    </li>
                    <li>
                      {t(
                        'Feishu limits each bot to 100 messages per minute and 5 per second; alerts beyond that are queued and reported as a merged count.'
                      )}
                    </li>
                  </ul>
                </AlertDescription>
              </Alert>

              {testState.ok === true ? (
                <Alert variant='default' className='flex items-center gap-2'>
                  <CheckCircle2 className='size-4 text-green-600' />
                  <div>
                    <AlertTitle>{t('Test alert sent')}</AlertTitle>
                    <AlertDescription>
                      {t('A test message was delivered to your Feishu group.')}
                    </AlertDescription>
                  </div>
                </Alert>
              ) : null}

              {testState.ok === false && testState.error ? (
                <Alert
                  variant='destructive'
                  className='flex items-center gap-2'
                >
                  <XCircle className='size-4' />
                  <div>
                    <AlertTitle>{t('Failed to send test alert')}</AlertTitle>
                    {/* Server/Feishu message rendered verbatim — running it
                        through t() would be meaningless and hide the real code. */}
                    <AlertDescription>{testState.error}</AlertDescription>
                  </div>
                </Alert>
              ) : null}
            </>
          ) : null}
        </SettingsForm>
      </Form>
    </SettingsSection>
  )
}
