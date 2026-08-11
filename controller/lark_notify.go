package controller

import (
	"bytes"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

// TestLarkNotify 发送一条飞书测试告警，供管理后台的「发送测试告警」按钮使用。
//
// 允许请求体为空或部分字段为空：缺失的字段回落到已存配置。
// 这个回落是必需的 —— GetOptions 会过滤掉 sign_secret，表单里的密钥框加载时永远是空的，
// 若不回落，任何开启了签名校验的机器人都会测试失败。
func TestLarkNotify(c *gin.Context) {
	var req struct {
		WebhookUrl string `json:"webhook_url"`
		SignSecret string `json:"sign_secret"`
	}

	rawBody, err := c.GetRawData()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if len(bytes.TrimSpace(rawBody)) > 0 {
		if err := common.Unmarshal(rawBody, &req); err != nil {
			common.ApiErrorMsg(c, "invalid request payload")
			return
		}
	}

	setting := operation_setting.GetLarkNotifySetting()

	webhookUrl := strings.TrimSpace(req.WebhookUrl)
	if webhookUrl == "" {
		webhookUrl = strings.TrimSpace(setting.WebhookUrl)
	}
	if webhookUrl == "" {
		common.ApiErrorMsg(c, "webhook_url is required")
		return
	}

	signSecret := strings.TrimSpace(req.SignSecret)
	if signSecret == "" {
		signSecret = setting.SignSecret
	}

	// 测试按钮不走通知限流：一个被限流后静默无响应的测试按钮比没有更糟。
	// 飞书返回的错误信息原样回传，配错时前端能直接看到飞书的错误码。
	if err := service.SendLarkTestNotify(webhookUrl, signSecret); err != nil {
		common.ApiErrorMsg(c, err.Error())
		return
	}

	common.ApiSuccess(c, nil)
}
