package operation_setting

import (
	"github.com/QuantumNous/new-api/setting/config"
)

// LarkNotifySetting 飞书（Lark）自定义群机器人告警配置
//
// 注意 SignSecret 的键名以 secret 结尾：controller/option.go 的 GetOptions
// 会过滤掉所有以 Token/Secret/Key/secret/api_key 结尾的键，因此该字段是只写不读的，
// 前端永远拿不到明文（与 SMTPToken 同一套语义）。
type LarkNotifySetting struct {
	Enabled            bool   `json:"enabled"`               // 总开关
	WebhookUrl         string `json:"webhook_url"`           // 飞书群机器人 webhook 地址
	SignSecret         string `json:"sign_secret"`           // 可选签名密钥，留空表示机器人未开启签名校验
	AlertOnRelayError  bool   `json:"alert_on_relay_error"`  // LLM 调用失败是否告警
	AlertOnChannelTest bool   `json:"alert_on_channel_test"` // 渠道测试失败是否告警
	ThrottleSeconds    int    `json:"throttle_seconds"`      // 同一渠道+模型+状态码的去重窗口，0 表示不去重
	UseCard            bool   `json:"use_card"`              // true 使用 interactive 卡片，false 使用纯文本
}

var larkNotifySetting = LarkNotifySetting{
	Enabled:            false,
	WebhookUrl:         "",
	SignSecret:         "",
	AlertOnRelayError:  true,
	AlertOnChannelTest: false,
	ThrottleSeconds:    0,
	UseCard:            true,
}

func init() {
	// 注册到全局配置管理器，DB 中对应 lark_notify_setting.* 点号键
	config.GlobalConfig.Register("lark_notify_setting", &larkNotifySetting)
}

func GetLarkNotifySetting() *LarkNotifySetting {
	return &larkNotifySetting
}
