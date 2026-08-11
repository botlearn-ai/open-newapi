package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/bytedance/gopkg/util/gopool"
)

const (
	// FailureAlertSourceRelay 中继请求（含任务中继）
	FailureAlertSourceRelay = "relay"
	// FailureAlertSourceChannelTest 渠道测试
	FailureAlertSourceChannelTest = "channel_test"
)

// 飞书自定义群机器人硬限流：100 次/分钟、5 次/秒，超限返回错误码 11232。
// 因此告警不能裸着并发发送，必须定速。
const (
	larkAlertIntervalMs      = 200 // 5 次/秒
	larkAlertPerMinute       = 100
	larkAlertQueueSize       = 1024
	larkAlertDedupeKeyPrefix = "lark_alert_dedupe:"
)

// FailureAlert 一次 LLM 调用失败的告警载荷
type FailureAlert struct {
	ChannelId   int
	ChannelType int
	ChannelName string
	UserId      int
	StatusCode  int
	RetryIndex  int
	ModelName   string
	ErrorType   string
	ErrorCode   string
	Message     string
	TokenName   string
	Group       string
	RequestPath string
	Source      string
	IsMultiKey  bool
	OccurredAt  time.Time
}

var (
	larkAlertQueue      = make(chan FailureAlert, larkAlertQueueSize)
	larkAlertSenderOnce sync.Once
	// mergedAlerts 记录因队列满而未能单独成条的告警数量。
	// 这些告警不会被静默丢弃，而是以聚合计数的形式附在下一条消息尾部。
	mergedAlerts    int64
	larkMinuteLimit common.InMemoryRateLimiter
	// dedupeStore 是 Redis 不可用时的去重回落存储
	dedupeStore     sync.Map
	dedupeCleanOnce sync.Once
)

// ShouldAlertFailure 判断该来源的失败是否需要告警。
// 放在构造 FailureAlert 之前调用，避免关闭状态下白白构造结构体。
func ShouldAlertFailure(source string) bool {
	setting := operation_setting.GetLarkNotifySetting()
	if !setting.Enabled || strings.TrimSpace(setting.WebhookUrl) == "" {
		return false
	}
	switch source {
	case FailureAlertSourceRelay:
		return setting.AlertOnRelayError
	case FailureAlertSourceChannelTest:
		return setting.AlertOnChannelTest
	default:
		return false
	}
}

// AlertLLMFailure 投递一条失败告警。非阻塞：绝不拖慢请求链路。
//
// 整个投递（含去重判断）都放在 gopool 里异步执行，因为开启去重时
// acquireAlertDedupeSlot 会做一次 Redis SetNX —— 那是网络调用，
// 绝不能出现在请求链路上。
func AlertLLMFailure(alert FailureAlert) {
	if !ShouldAlertFailure(alert.Source) {
		return
	}
	gopool.Go(func() {
		enqueueFailureAlert(alert)
	})
}

func enqueueFailureAlert(alert FailureAlert) {
	setting := operation_setting.GetLarkNotifySetting()

	// 去重窗口（可选）。默认 ThrottleSeconds=0，即每次失败都发。
	if setting.ThrottleSeconds > 0 && !acquireAlertDedupeSlot(alert, setting.ThrottleSeconds) {
		return
	}

	larkAlertSenderOnce.Do(startLarkAlertSender)

	select {
	case larkAlertQueue <- alert:
	default:
		// 队列满：只累加计数，绝不阻塞，也绝不静默丢弃 —— 计数会随下一条消息上报
		atomic.AddInt64(&mergedAlerts, 1)
	}
}

// acquireAlertDedupeSlot 按 渠道+模型+状态码 做窗口去重。
// 多节点部署下必须一致，所以优先用 Redis SetNX（原子），否则回落进程内存。
func acquireAlertDedupeSlot(alert FailureAlert, throttleSeconds int) bool {
	key := fmt.Sprintf("%s%d:%s:%d", larkAlertDedupeKeyPrefix, alert.ChannelId, alert.ModelName, alert.StatusCode)
	ttl := time.Duration(throttleSeconds) * time.Second

	if common.RedisEnabled && common.RDB != nil {
		ok, err := common.RDB.SetNX(context.Background(), key, "1", ttl).Result()
		if err == nil {
			return ok
		}
		// Redis 异常时不因为去重失败而丢告警，回落到内存判断
		common.SysError("lark alert dedupe via redis failed, falling back to memory: " + err.Error())
	}

	dedupeCleanOnce.Do(startDedupeCleanup)
	now := time.Now()
	if value, loaded := dedupeStore.Load(key); loaded {
		if expireAt, valid := value.(time.Time); valid && now.Before(expireAt) {
			return false
		}
	}
	dedupeStore.Store(key, now.Add(ttl))
	return true
}

func startDedupeCleanup() {
	gopool.Go(func() {
		for {
			time.Sleep(10 * time.Minute)
			now := time.Now()
			dedupeStore.Range(func(key, value any) bool {
				if expireAt, ok := value.(time.Time); ok && now.After(expireAt) {
					dedupeStore.Delete(key)
				}
				return true
			})
		}
	})
}

// startLarkAlertSender 启动定速发送协程。
//
// pending 的作用：先从队列取出告警，再申请分钟额度。若额度用尽则保留 pending
// 到下个 tick 重试，这样既不会丢消息，也不会在队列为空时白白消耗额度。
func startLarkAlertSender() {
	larkMinuteLimit.Init(2 * time.Minute)
	gopool.Go(func() {
		ticker := time.NewTicker(larkAlertIntervalMs * time.Millisecond)
		defer ticker.Stop()

		var pending *FailureAlert
		for range ticker.C {
			if pending == nil {
				select {
				case alert := <-larkAlertQueue:
					pending = &alert
				default:
					continue
				}
			}
			if !larkMinuteLimit.Request("lark_alert", larkAlertPerMinute, 60) {
				// 本分钟额度用尽，pending 留到下个 tick，消息不丢
				continue
			}
			sendFailureAlert(*pending)
			pending = nil
		}
	})
}

// sendFailureAlert 实际发送。
//
// 自激防护：这里的任何失败只允许写 SysError，严禁回调 AlertLLMFailure，
// 否则 webhook 挂掉会把自身错误放大成死循环。
func sendFailureAlert(alert FailureAlert) {
	setting := operation_setting.GetLarkNotifySetting()
	webhookUrl := strings.TrimSpace(setting.WebhookUrl)
	if webhookUrl == "" {
		return
	}

	merged := atomic.SwapInt64(&mergedAlerts, 0)
	title, content, template := buildFailureAlertMessage(alert, merged)

	var err error
	if setting.UseCard {
		err = SendLarkCardNotify(webhookUrl, setting.SignSecret, title, content, template)
	} else {
		err = SendLarkNotify(webhookUrl, setting.SignSecret, dtoNotifyForAlert(title, content))
	}
	if err != nil {
		// 发送失败时把合并计数还回去，避免这批告警的数量信息丢失
		if merged > 0 {
			atomic.AddInt64(&mergedAlerts, merged)
		}
		common.SysError("failed to send lark failure alert: " + err.Error())
	}
}

// buildFailureAlertMessage 组装值班需要的最小充分信息集
func buildFailureAlertMessage(alert FailureAlert, merged int64) (title string, content string, template string) {
	template = "red"
	switch {
	case alert.StatusCode >= 500:
		template = "red"
	case alert.StatusCode == 429:
		template = "orange"
	case alert.StatusCode >= 400:
		template = "orange"
	}

	sourceLabel := "中继请求"
	if alert.Source == FailureAlertSourceChannelTest {
		sourceLabel = "渠道测试"
	}

	title = fmt.Sprintf("LLM 调用失败：%s (#%d)", alert.ChannelName, alert.ChannelId)

	occurredAt := alert.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	lines := []string{
		fmt.Sprintf("**渠道**：%s（#%d，type=%d）", alert.ChannelName, alert.ChannelId, alert.ChannelType),
		fmt.Sprintf("**模型**：%s", fallbackText(alert.ModelName)),
		fmt.Sprintf("**状态码**：%d", alert.StatusCode),
		fmt.Sprintf("**错误类型**：%s", fallbackText(alert.ErrorType)),
		fmt.Sprintf("**错误码**：%s", fallbackText(alert.ErrorCode)),
		fmt.Sprintf("**来源**：%s（第 %d 次尝试）", sourceLabel, alert.RetryIndex+1),
		fmt.Sprintf("**用户**：#%d　**令牌**：%s　**分组**：%s", alert.UserId, fallbackText(alert.TokenName), fallbackText(alert.Group)),
		fmt.Sprintf("**请求路径**：%s", fallbackText(alert.RequestPath)),
		fmt.Sprintf("**时间**：%s", occurredAt.Format("2006-01-02 15:04:05")),
	}
	if alert.IsMultiKey {
		lines = append(lines, "**多密钥渠道**：是")
	}
	if alert.Message != "" {
		lines = append(lines, "", fmt.Sprintf("**错误信息**：%s", common.LocalLogPreview(alert.Message)))
	}
	if merged > 0 {
		lines = append(lines, "", fmt.Sprintf("_另有 %d 条告警因飞书限流被合并（飞书上限 100 次/分钟、5 次/秒）_", merged))
	}

	return title, strings.Join(lines, "\n"), template
}

func fallbackText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// dtoNotifyForAlert 把告警包成 dto.Notify 以复用纯文本发送路径。
// Values 传 nil：内容已经拼好，无需 {{value}} 占位符替换。
func dtoNotifyForAlert(title string, content string) dto.Notify {
	return dto.NewNotify(dto.NotifyTypeLLMFailure, title, content, nil)
}
