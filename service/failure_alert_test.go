package service

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func withLarkSetting(t *testing.T, mutate func(s *operation_setting.LarkNotifySetting)) {
	t.Helper()
	s := operation_setting.GetLarkNotifySetting()
	original := *s
	t.Cleanup(func() { *s = original })
	mutate(s)
}

func TestShouldAlertFailureGating(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *operation_setting.LarkNotifySetting)
		source string
		want   bool
	}{
		{
			name: "disabled globally",
			mutate: func(s *operation_setting.LarkNotifySetting) {
				s.Enabled = false
				s.WebhookUrl = "https://open.feishu.cn/x"
				s.AlertOnRelayError = true
			},
			source: FailureAlertSourceRelay,
			want:   false,
		},
		{
			// 开了总开关但没填地址：必须视为未配置，否则每次失败都会走一遍无效发送
			name: "enabled but no webhook url",
			mutate: func(s *operation_setting.LarkNotifySetting) {
				s.Enabled = true
				s.WebhookUrl = "   "
				s.AlertOnRelayError = true
			},
			source: FailureAlertSourceRelay,
			want:   false,
		},
		{
			name: "relay enabled",
			mutate: func(s *operation_setting.LarkNotifySetting) {
				s.Enabled = true
				s.WebhookUrl = "https://open.feishu.cn/x"
				s.AlertOnRelayError = true
			},
			source: FailureAlertSourceRelay,
			want:   true,
		},
		{
			name: "channel test disabled by default",
			mutate: func(s *operation_setting.LarkNotifySetting) {
				s.Enabled = true
				s.WebhookUrl = "https://open.feishu.cn/x"
				s.AlertOnChannelTest = false
			},
			source: FailureAlertSourceChannelTest,
			want:   false,
		},
		{
			name: "channel test enabled",
			mutate: func(s *operation_setting.LarkNotifySetting) {
				s.Enabled = true
				s.WebhookUrl = "https://open.feishu.cn/x"
				s.AlertOnChannelTest = true
			},
			source: FailureAlertSourceChannelTest,
			want:   true,
		},
		{
			name: "unknown source never alerts",
			mutate: func(s *operation_setting.LarkNotifySetting) {
				s.Enabled = true
				s.WebhookUrl = "https://open.feishu.cn/x"
				s.AlertOnRelayError = true
				s.AlertOnChannelTest = true
			},
			source: "something_else",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withLarkSetting(t, tc.mutate)
			if got := ShouldAlertFailure(tc.source); got != tc.want {
				t.Fatalf("ShouldAlertFailure(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

func TestBuildFailureAlertMessageSeverityColour(t *testing.T) {
	cases := []struct {
		statusCode int
		want       string
	}{
		{500, "red"},
		{502, "red"},
		{429, "orange"},
		{401, "orange"},
		{0, "red"}, // 网络层失败没有状态码，按最高严重度处理
	}
	for _, tc := range cases {
		_, _, template := buildFailureAlertMessage(FailureAlert{StatusCode: tc.statusCode}, 0)
		if template != tc.want {
			t.Fatalf("status %d -> template %q, want %q", tc.statusCode, template, tc.want)
		}
	}
}

func TestBuildFailureAlertMessageContent(t *testing.T) {
	alert := FailureAlert{
		ChannelId:   7,
		ChannelType: 1,
		ChannelName: "my-channel",
		UserId:      42,
		StatusCode:  503,
		ModelName:   "gpt-4o",
		ErrorType:   "upstream_error",
		ErrorCode:   "bad_response_status_code",
		Message:     "upstream unavailable",
		TokenName:   "tok",
		Group:       "default",
		RequestPath: "/v1/chat/completions",
		Source:      FailureAlertSourceRelay,
		OccurredAt:  time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC),
	}

	title, content, _ := buildFailureAlertMessage(alert, 0)

	if !strings.Contains(title, "my-channel") || !strings.Contains(title, "#7") {
		t.Fatalf("title missing channel identity: %q", title)
	}

	// 值班定位问题所必需的字段，缺一个都算回归
	for _, want := range []string{
		"my-channel", "gpt-4o", "503", "upstream_error",
		"bad_response_status_code", "/v1/chat/completions",
		"#42", "tok", "default", "upstream unavailable", "中继请求",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("content missing %q:\n%s", want, content)
		}
	}

	// 合并计数为 0 时不应出现限流提示
	if strings.Contains(content, "合并") {
		t.Fatalf("merged notice must be absent when merged == 0:\n%s", content)
	}
}

func TestBuildFailureAlertMessageReportsMergedCount(t *testing.T) {
	_, content, _ := buildFailureAlertMessage(FailureAlert{StatusCode: 500}, 17)
	if !strings.Contains(content, "17") || !strings.Contains(content, "合并") {
		t.Fatalf("merged count must be reported, never silently dropped:\n%s", content)
	}
}

func TestBuildFailureAlertMessageFallsBackForEmptyFields(t *testing.T) {
	_, content, _ := buildFailureAlertMessage(FailureAlert{StatusCode: 500}, 0)
	// 空字段渲染成 "-"，避免出现 "**模型**：" 这种断尾
	if !strings.Contains(content, "-") {
		t.Fatalf("empty fields should render as '-':\n%s", content)
	}
	if strings.Contains(content, "：\n") {
		t.Fatalf("found a field with an empty value:\n%s", content)
	}
}

// 队列满时必须累加合并计数，绝不能阻塞调用方
func TestEnqueueFailureAlertNeverBlocksWhenQueueFull(t *testing.T) {
	withLarkSetting(t, func(s *operation_setting.LarkNotifySetting) {
		s.Enabled = true
		s.WebhookUrl = "https://open.feishu.cn/x"
		s.AlertOnRelayError = true
		s.ThrottleSeconds = 0
	})

	// 用一个满的本地队列替换，避免启动真实发送协程
	originalQueue := larkAlertQueue
	larkAlertQueue = make(chan FailureAlert, 1)
	larkAlertQueue <- FailureAlert{}
	t.Cleanup(func() { larkAlertQueue = originalQueue })

	atomic.StoreInt64(&mergedAlerts, 0)
	t.Cleanup(func() { atomic.StoreInt64(&mergedAlerts, 0) })

	done := make(chan struct{})
	go func() {
		enqueueFailureAlert(FailureAlert{ChannelId: 1, Source: FailureAlertSourceRelay})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueueFailureAlert blocked on a full queue")
	}

	if got := atomic.LoadInt64(&mergedAlerts); got != 1 {
		t.Fatalf("mergedAlerts = %d, want 1", got)
	}
}

// 去重：Redis 不可用时回落到内存，同 key 在窗口内只放行一次
func TestAcquireAlertDedupeSlotMemoryFallback(t *testing.T) {
	alert := FailureAlert{ChannelId: 99, ModelName: "gpt-4o", StatusCode: 500}

	key := "lark_alert_dedupe:99:gpt-4o:500"
	dedupeStore.Delete(key)
	t.Cleanup(func() { dedupeStore.Delete(key) })

	if !acquireAlertDedupeSlot(alert, 60) {
		t.Fatal("first acquire must succeed")
	}
	if acquireAlertDedupeSlot(alert, 60) {
		t.Fatal("second acquire within the window must be suppressed")
	}

	// 不同状态码是不同的 key，不应被前一次去重挡住
	other := alert
	other.StatusCode = 429
	otherKey := "lark_alert_dedupe:99:gpt-4o:429"
	dedupeStore.Delete(otherKey)
	t.Cleanup(func() { dedupeStore.Delete(otherKey) })
	if !acquireAlertDedupeSlot(other, 60) {
		t.Fatal("a different status code must not be deduped against another key")
	}
}

func TestAcquireAlertDedupeSlotWindowExpiry(t *testing.T) {
	alert := FailureAlert{ChannelId: 98, ModelName: "m", StatusCode: 500}
	key := "lark_alert_dedupe:98:m:500"
	dedupeStore.Delete(key)
	t.Cleanup(func() { dedupeStore.Delete(key) })

	// 用一个已过期的时间戳预置，模拟窗口已经走完
	dedupeStore.Store(key, time.Now().Add(-time.Second))
	if !acquireAlertDedupeSlot(alert, 1) {
		t.Fatal("an expired window must allow the next alert through")
	}
}
