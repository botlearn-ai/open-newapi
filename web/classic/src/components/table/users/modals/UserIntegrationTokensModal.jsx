/*
Copyright (C) 2025 QuantumNous

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

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  Button,
  InputNumber,
  Modal,
  Select,
  Space,
  Spin,
  Typography,
} from '@douyinfe/semi-ui';
import {
  API,
  getCurrencyConfig,
  renderQuota,
  showError,
  showSuccess,
} from '../../../../helpers';
import {
  displayAmountToQuota,
  getQuotaPerUnit,
} from '../../../../helpers/quota';
import { useIsMobile } from '../../../../hooks/common/useIsMobile';

const UserIntegrationTokensModal = (props) => {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [tokenId, setTokenId] = useState(null);
  const [amount, setAmount] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const generation = useRef(0);
  const requests = useRef(new Map());
  const submitLock = useRef(false);

  const load = useCallback(async () => {
    const current = ++generation.current;
    setLoading(true);
    setFailed(false);
    try {
      const res = await API.get(`/api/user/${props.userId}/integration-tokens`);
      if (current !== generation.current) return;
      if (!res.data.success) throw new Error(res.data.message);
      setData(res.data.data);
      setTokenId((previous) =>
        res.data.data.tokens.some((token) => token.token_id === previous)
          ? previous
          : (res.data.data.tokens[0]?.token_id ?? null),
      );
    } catch {
      if (current === generation.current) setFailed(true);
    } finally {
      if (current === generation.current) setLoading(false);
    }
  }, [props.userId]);

  useEffect(() => {
    setData(null);
    setAmount('');
    setTokenId(null);
    if (props.visible && props.userId) void load();
    return () => {
      generation.current += 1;
    };
  }, [props.visible, props.userId, load]);

  const token = data?.tokens.find((item) => item.token_id === tokenId);
  const quota = displayAmountToQuota(amount);
  const valid =
    Number(amount) > 0 &&
    Number.isSafeInteger(quota) &&
    quota > 0 &&
    quota <= 1e9 * getQuotaPerUnit();
  const currency = getCurrencyConfig();
  let status = t('未知');
  if (token?.status === 1) status = t('已启用');
  if (token?.status === 2) status = t('已禁用');
  if (token?.status === 4) status = t('已用尽');
  if (
    token?.status === 3 ||
    (token &&
      token.expired_time !== -1 &&
      token.expired_time <= Date.now() / 1000)
  )
    status = t('已过期');

  const submit = async () => {
    if (!valid || !token || token.unlimited_quota || submitLock.current) return;
    submitLock.current = true;
    setSubmitting(true);
    const requestId = `${props.userId}:${token.token_id}:${quota}`;
    if (!requests.current.has(requestId))
      requests.current.set(requestId, crypto.randomUUID());
    try {
      const res = await API.post(
        `/api/user/${props.userId}/integration-tokens/${token.token_id}/quota`,
        { quota },
        { headers: { 'Idempotency-Key': requests.current.get(requestId) } },
      );
      if (!res.data.success) throw new Error(res.data.message);
      requests.current.delete(requestId);
      setAmount('');
      showSuccess(t('Token quota added successfully'));
      await load();
    } catch {
      showError(
        t(
          'Failed to add token quota. Retry with the same amount to avoid duplicate credit.',
        ),
      );
    } finally {
      submitLock.current = false;
      setSubmitting(false);
    }
  };

  return (
    <Modal
      title={`${t('Integration token quota')} · #${props.userId}`}
      visible={props.visible}
      onCancel={() => {
        if (!submitting) props.onCancel();
      }}
      footer={null}
      bodyStyle={{ paddingBottom: 24, maxHeight: '70vh', overflowY: 'auto' }}
      centered
      width={isMobile ? '95%' : 520}
      closeOnEsc={!submitting}
      maskClosable={!submitting}
    >
      <Space vertical align='start' spacing={16} style={{ width: '100%' }}>
        <Typography.Text type='secondary'>
          {t(
            'Add quota only to the selected token. The user balance and used quota will not change.',
          )}
        </Typography.Text>
        {loading && <Spin />}
        {failed && (
          <div role='alert'>
            <Typography.Text type='danger'>
              {t('Failed to load integration tokens')}
            </Typography.Text>
            <Button onClick={load}>{t('重试')}</Button>
          </div>
        )}
        {data && !failed && (
          <>
            <Typography.Text>
              {t('User balance')}: {renderQuota(data.user_quota, 6)}
            </Typography.Text>
            {data.tokens.length === 0 && (
              <Typography.Text>
                {t('No linked integration tokens')}
              </Typography.Text>
            )}
            {data.tokens.length > 0 && (
              <Select
                aria-label={t('Integration token quota')}
                style={{ width: '100%' }}
                value={tokenId}
                disabled={submitting || loading}
                optionList={data.tokens.map((item) => ({
                  value: item.token_id,
                  label: `${item.name} · #${item.token_id} · ${item.integration_id}`,
                }))}
                onChange={(value) => {
                  setTokenId(value);
                  setAmount('');
                }}
              />
            )}
            {token && (
              <>
                <Typography.Text>
                  {token.integration_id} · {status}
                </Typography.Text>
                <Typography.Text>
                  {t('Token remaining quota')}:{' '}
                  {token.unlimited_quota
                    ? t('无限额度')
                    : renderQuota(token.remain_quota, 6)}
                </Typography.Text>
                {!token.unlimited_quota && (
                  <>
                    <label htmlFor={`integration-token-amount-${props.userId}`}>
                      {t('Amount to add')} ({currency.type})
                    </label>
                    <InputNumber
                      id={`integration-token-amount-${props.userId}`}
                      aria-label={t('Amount to add')}
                      value={amount}
                      onChange={setAmount}
                      min={0}
                      precision={currency.type === 'TOKENS' ? 0 : 6}
                      step={currency.type === 'TOKENS' ? 1 : 0.000001}
                      prefix={currency.symbol}
                      disabled={submitting || loading}
                      style={{ width: '100%' }}
                    />
                    {valid && (
                      <Typography.Text>
                        {renderQuota(token.remain_quota, 6)} +{' '}
                        {renderQuota(quota, 6)} ={' '}
                        {renderQuota(token.remain_quota + quota, 6)}
                      </Typography.Text>
                    )}
                    <Button
                      theme='solid'
                      type='primary'
                      block
                      loading={submitting}
                      disabled={!valid || loading}
                      onClick={submit}
                    >
                      {t('Confirm token quota increase')}
                    </Button>
                  </>
                )}
              </>
            )}
          </>
        )}
      </Space>
    </Modal>
  );
};

export default UserIntegrationTokensModal;
