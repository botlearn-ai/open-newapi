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

import React, { useEffect, useState, useRef } from 'react';
import { Banner, Button, Col, Form, Row, Space, Spin } from '@douyinfe/semi-ui';
import {
  compareObjects,
  API,
  showError,
  showSuccess,
  showWarning,
} from '../../../helpers';
import { useTranslation } from 'react-i18next';

export default function SettingsLarkNotify(props) {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [testLoading, setTestLoading] = useState(false);
  // 测试结果单独展示，不用 toast —— 飞书的错误码需要留在屏幕上便于排查
  const [testResult, setTestResult] = useState(null);

  // 注意：这里不包含 lark_notify_setting.sign_secret。
  // GetOptions 会过滤掉以 secret 结尾的键，所以它永远不会出现在 props.options 里；
  // 若把它放进 inputs 参与 compareObjects 差异比较，保存时会把空串推上去覆盖已存密钥。
  const [inputs, setInputs] = useState({
    'lark_notify_setting.enabled': false,
    'lark_notify_setting.webhook_url': '',
    'lark_notify_setting.alert_on_relay_error': true,
    'lark_notify_setting.alert_on_channel_test': false,
    'lark_notify_setting.throttle_seconds': 0,
    'lark_notify_setting.use_card': true,
  });
  // 签名密钥是只写字段：独立于 inputs，仅在非空时提交
  const [signSecret, setSignSecret] = useState('');
  const refForm = useRef();
  const [inputsRow, setInputsRow] = useState(inputs);

  function onSubmit() {
    const updateArray = compareObjects(inputs, inputsRow);
    const trimmedSecret = (signSecret || '').trim();
    if (!updateArray.length && !trimmedSecret) {
      return showWarning(t('你似乎并没有修改什么'));
    }

    const webhookUrl = (inputs['lark_notify_setting.webhook_url'] || '').trim();
    if (inputs['lark_notify_setting.enabled'] && !webhookUrl) {
      return showError(t('启用飞书告警时必须填写 Webhook 地址'));
    }
    if (webhookUrl && !/^https:\/\//i.test(webhookUrl)) {
      return showError(t('飞书 Webhook 地址必须以 https:// 开头'));
    }

    const requestQueue = updateArray.map((item) => {
      let value = inputs[item.key];
      if (typeof value === 'boolean') {
        value = String(value);
      } else if (item.key === 'lark_notify_setting.webhook_url') {
        value = webhookUrl;
      } else {
        value = String(value);
      }
      return API.put('/api/option/', { key: item.key, value });
    });

    // 留空表示沿用已存密钥；只有填了新值才提交
    if (trimmedSecret) {
      requestQueue.push(
        API.put('/api/option/', {
          key: 'lark_notify_setting.sign_secret',
          value: trimmedSecret,
        }),
      );
    }

    setLoading(true);
    Promise.all(requestQueue)
      .then((res) => {
        if (res.includes(undefined)) {
          return showError(t('部分保存失败，请重试'));
        }
        showSuccess(t('保存成功'));
        setSignSecret('');
        refForm.current?.setValue('lark_notify_setting.sign_secret', '');
        props.refresh();
      })
      .catch(() => {
        showError(t('保存失败，请重试'));
      })
      .finally(() => {
        setLoading(false);
      });
  }

  function onClearSecret() {
    setLoading(true);
    API.put('/api/option/', {
      key: 'lark_notify_setting.sign_secret',
      value: '',
    })
      .then((res) => {
        if (res === undefined) return;
        showSuccess(t('签名密钥已清除'));
        setSignSecret('');
        refForm.current?.setValue('lark_notify_setting.sign_secret', '');
      })
      .catch(() => showError(t('保存失败，请重试')))
      .finally(() => setLoading(false));
  }

  // 发送测试告警。请求体里的空字段由后端回落到已存配置，
  // 这对签名密钥是必需的 —— 表单里的密钥框加载时永远是空的。
  function onSendTest() {
    const webhookUrl = (inputs['lark_notify_setting.webhook_url'] || '').trim();
    if (!webhookUrl) {
      return showError(t('发送测试告警前需要填写 Webhook 地址'));
    }
    setTestLoading(true);
    setTestResult(null);
    API.post(
      '/api/option/lark_notify/test',
      {
        webhook_url: webhookUrl,
        sign_secret: (signSecret || '').trim(),
      },
      // 结果用下方 Banner 展示；不跳过的话拦截器会对网络/5xx 再弹一个 toast
      { skipErrorHandler: true },
    )
      .then((res) => {
        if (res === undefined) {
          setTestResult({ ok: false, message: t('发送测试告警失败') });
          return;
        }
        const { success, message } = res.data || {};
        setTestResult(
          success
            ? { ok: true, message: t('已向你的飞书群发送一条测试消息') }
            : { ok: false, message: message || t('发送测试告警失败') },
        );
      })
      .catch((error) => {
        setTestResult({
          ok: false,
          message:
            error?.response?.data?.message ||
            error?.message ||
            t('发送测试告警失败'),
        });
      })
      .finally(() => setTestLoading(false));
  }

  useEffect(() => {
    const currentInputs = {};
    for (let key in props.options) {
      if (Object.keys(inputs).includes(key)) {
        currentInputs[key] = props.options[key];
      }
    }
    setInputs(currentInputs);
    setInputsRow(structuredClone(currentInputs));
    refForm.current?.setValues(currentInputs);
  }, [props.options]);

  return (
    <>
      <Spin spinning={loading}>
        <Form
          values={inputs}
          getFormApi={(formAPI) => (refForm.current = formAPI)}
          style={{ marginBottom: 15 }}
        >
          <Form.Section text={t('飞书告警')}>
            <Row gutter={16}>
              <Col xs={24} sm={12} md={8} lg={8} xl={8}>
                <Form.Switch
                  field={'lark_notify_setting.enabled'}
                  label={t('启用飞书告警')}
                  size='default'
                  checkedText='｜'
                  uncheckedText='〇'
                  extraText={t(
                    '通过自定义群机器人 Webhook 把失败告警推送到飞书群',
                  )}
                  onChange={(value) =>
                    setInputs({
                      ...inputs,
                      'lark_notify_setting.enabled': value,
                    })
                  }
                />
              </Col>
              <Col xs={24} sm={12} md={8} lg={8} xl={8}>
                <Form.Switch
                  field={'lark_notify_setting.use_card'}
                  label={t('使用富文本卡片')}
                  size='default'
                  checkedText='｜'
                  uncheckedText='〇'
                  extraText={t(
                    '发送带颜色标识的卡片而非纯文本，若机器人拒收卡片请关闭',
                  )}
                  onChange={(value) =>
                    setInputs({
                      ...inputs,
                      'lark_notify_setting.use_card': value,
                    })
                  }
                />
              </Col>
            </Row>
            <Row gutter={16}>
              <Col xs={24} sm={16}>
                <Form.Input
                  label={t('飞书 Webhook 地址')}
                  placeholder='https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxxxx'
                  extraText={t('飞书群自定义机器人的 Webhook 地址')}
                  field={'lark_notify_setting.webhook_url'}
                  showClear
                  onChange={(value) =>
                    setInputs({
                      ...inputs,
                      'lark_notify_setting.webhook_url': value,
                    })
                  }
                />
              </Col>
            </Row>
            <Row gutter={16}>
              <Col xs={24} sm={16}>
                <Form.Input
                  label={t('签名密钥')}
                  mode='password'
                  placeholder={t('输入新密钥以更新')}
                  extraText={t(
                    '留空则保留现有密钥，仅当机器人开启了签名校验时才需要填写',
                  )}
                  field={'lark_notify_setting.sign_secret'}
                  autoComplete='off'
                  onChange={(value) => setSignSecret(value)}
                />
              </Col>
            </Row>
            <Row gutter={16}>
              <Col xs={24} sm={12} md={8} lg={8} xl={8}>
                <Form.Switch
                  field={'lark_notify_setting.alert_on_relay_error'}
                  label={t('LLM 调用失败时告警')}
                  size='default'
                  checkedText='｜'
                  uncheckedText='〇'
                  extraText={t('上游 LLM 请求失败时推送告警')}
                  onChange={(value) =>
                    setInputs({
                      ...inputs,
                      'lark_notify_setting.alert_on_relay_error': value,
                    })
                  }
                />
              </Col>
              <Col xs={24} sm={12} md={8} lg={8} xl={8}>
                <Form.Switch
                  field={'lark_notify_setting.alert_on_channel_test'}
                  label={t('渠道测试失败时告警')}
                  size='default'
                  checkedText='｜'
                  uncheckedText='〇'
                  extraText={t('定时渠道测试失败并触发禁用时也发送告警')}
                  onChange={(value) =>
                    setInputs({
                      ...inputs,
                      'lark_notify_setting.alert_on_channel_test': value,
                    })
                  }
                />
              </Col>
            </Row>
            <Row gutter={16}>
              <Col xs={24} sm={12} md={8} lg={8} xl={8}>
                <Form.InputNumber
                  label={t('告警节流间隔')}
                  step={1}
                  min={0}
                  suffix={t('秒')}
                  extraText={t(
                    '在此时间窗内对相同渠道、模型和状态码的告警去重，设为 0 表示每次失败都告警',
                  )}
                  placeholder={''}
                  field={'lark_notify_setting.throttle_seconds'}
                  onChange={(value) =>
                    setInputs({
                      ...inputs,
                      'lark_notify_setting.throttle_seconds': parseInt(value),
                    })
                  }
                />
              </Col>
            </Row>
            {testResult && (
              <Row gutter={16} style={{ marginBottom: 12 }}>
                <Col xs={24} sm={16}>
                  <Banner
                    type={testResult.ok ? 'success' : 'danger'}
                    closeIcon={null}
                    description={testResult.message}
                  />
                </Col>
              </Row>
            )}
            <Row>
              <Space>
                <Button size='default' onClick={onSubmit}>
                  {t('保存飞书告警设置')}
                </Button>
                <Button
                  size='default'
                  theme='light'
                  loading={testLoading}
                  onClick={onSendTest}
                >
                  {t('发送测试告警')}
                </Button>
                <Button
                  size='default'
                  theme='borderless'
                  onClick={onClearSecret}
                >
                  {t('清除签名密钥')}
                </Button>
              </Space>
            </Row>
            <Row style={{ marginTop: 12 }}>
              <Col xs={24} sm={16}>
                <Banner
                  fullMode={false}
                  type='info'
                  closeIcon={null}
                  title={t('如何创建飞书群机器人')}
                  description={
                    <div>
                      <div>
                        1.{' '}
                        {t(
                          '打开目标飞书群，进入 设置 > 群机器人 > 添加机器人 > 自定义机器人',
                        )}
                      </div>
                      <div>2. {t('复制生成的 Webhook 地址并粘贴到此处')}</div>
                      <div>3. {t('可选：开启签名校验并把密钥填到上方')}</div>
                      <div>
                        {t(
                          '飞书对单个机器人限制 100 条/分钟、5 条/秒；超出的告警会排队，并以合并计数的形式上报',
                        )}
                      </div>
                    </div>
                  }
                />
              </Col>
            </Row>
          </Form.Section>
        </Form>
      </Spin>
    </>
  );
}
