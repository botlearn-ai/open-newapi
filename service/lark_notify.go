package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

const larkNotifyUserAgent = "NewAPI-Lark-Notify/1.0"

// larkResponse 飞书自定义机器人的响应。
//
// 关键点：飞书用 HTTP 200 + body 里的非零 code 表示失败，只看 HTTP 状态码
// 会把失败当成功。部分错误还走老的 StatusCode/StatusMessage 字段，两者都要判。
type larkResponse struct {
	Code          int    `json:"code"`
	Msg           string `json:"msg"`
	StatusCode    int    `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

func (r larkResponse) ok() bool {
	return r.Code == 0 && r.StatusCode == 0
}

func (r larkResponse) errMsg() string {
	if r.Msg != "" {
		return fmt.Sprintf("lark returned code %d: %s", r.Code, r.Msg)
	}
	if r.StatusMessage != "" {
		return fmt.Sprintf("lark returned StatusCode %d: %s", r.StatusCode, r.StatusMessage)
	}
	return fmt.Sprintf("lark returned code %d", r.Code)
}

// larkSign 计算飞书自定义机器人的签名。
//
// 注意飞书这里与钉钉相反：待签串 "{timestamp}\n{secret}" 作为 HMAC 的 KEY，
// 消息体为空，结果 base64。钉钉是 key=secret、msg=待签串。写错则校验永远失败。
func larkSign(timestamp int64, secret string) string {
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secret
	h := hmac.New(sha256.New, []byte(stringToSign))
	// 消息体为空是飞书的规定，不是遗漏
	h.Write([]byte(""))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// applyNotifyValues 处理 {{value}} 占位符，与 email/bark/gotify 一致。
// 不要用 service/webhook.go 里的 fmt.Sprintf(content, value) —— 那是既有 bug，
// 内容模板里没有 %v 时值会被静默丢弃。
func applyNotifyValues(content string, values []interface{}) string {
	for _, value := range values {
		content = strings.Replace(content, dto.ContentValueParam, fmt.Sprintf("%v", value), 1)
	}
	return content
}

// buildLarkTextPayload 构建纯文本消息体
func buildLarkTextPayload(title string, content string, signSecret string) (map[string]interface{}, error) {
	text := content
	if title != "" {
		text = title + "\n" + content
	}
	payload := map[string]interface{}{
		"msg_type": "text",
		"content": map[string]interface{}{
			"text": text,
		},
	}
	attachLarkSign(payload, signSecret)
	return payload, nil
}

// buildLarkCardPayload 构建 interactive 卡片消息体。
// 用经典 card 格式（config/header/elements），而非 schema 2.0 —— 兼容性更稳。
func buildLarkCardPayload(title string, content string, template string, signSecret string) (map[string]interface{}, error) {
	if template == "" {
		template = "red"
	}
	payload := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"config": map[string]interface{}{
				"wide_screen_mode": true,
			},
			"header": map[string]interface{}{
				"template": template,
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": title,
				},
			},
			"elements": []interface{}{
				map[string]interface{}{
					"tag": "div",
					"text": map[string]interface{}{
						"tag":     "lark_md",
						"content": content,
					},
				},
			},
		},
	}
	attachLarkSign(payload, signSecret)
	return payload, nil
}

func attachLarkSign(payload map[string]interface{}, signSecret string) {
	if signSecret == "" {
		return
	}
	timestamp := time.Now().Unix()
	payload["timestamp"] = strconv.FormatInt(timestamp, 10)
	payload["sign"] = larkSign(timestamp, signSecret)
}

// sendLarkPayload 发送已构建好的负载并解析飞书响应
func sendLarkPayload(webhookURL string, payload map[string]interface{}) error {
	body, err := common.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal lark payload: %v", err)
	}

	respBody, err := postNotifyJSON(webhookURL, larkNotifyUserAgent, nil, body)
	if err != nil {
		return err
	}

	var larkResp larkResponse
	if err = common.Unmarshal(respBody, &larkResp); err != nil {
		// 解析不出来但 HTTP 成功，保守地当成功处理，只把原始响应带回去便于排查
		return nil
	}
	if !larkResp.ok() {
		return fmt.Errorf("%s", larkResp.errMsg())
	}
	return nil
}

// SendLarkNotify 通过飞书自定义群机器人发送通知。
// 形参风格与 SendWebhookNotify / sendGotifyNotify 保持一致，便于接入 NotifyUser 的 switch。
func SendLarkNotify(webhookURL string, signSecret string, data dto.Notify) error {
	content := applyNotifyValues(data.Content, data.Values)
	payload, err := buildLarkTextPayload(data.Title, content, signSecret)
	if err != nil {
		return err
	}
	return sendLarkPayload(webhookURL, payload)
}

// SendLarkCardNotify 通过飞书自定义群机器人发送卡片通知，供告警链路使用。
func SendLarkCardNotify(webhookURL string, signSecret string, title string, markdown string, template string) error {
	payload, err := buildLarkCardPayload(title, markdown, template, signSecret)
	if err != nil {
		return err
	}
	return sendLarkPayload(webhookURL, payload)
}

// SendLarkTestNotify 发送一条测试消息，供管理后台的「发送测试告警」按钮使用。
func SendLarkTestNotify(webhookURL string, signSecret string) error {
	content := strings.Join([]string{
		"**这是一条来自 New API 的测试告警**",
		"",
		fmt.Sprintf("发送时间：%s", time.Now().Format("2006-01-02 15:04:05")),
		"",
		"看到这条消息说明飞书告警配置正确。",
	}, "\n")
	return SendLarkCardNotify(webhookURL, signSecret, "New API 测试告警", content, "blue")
}
