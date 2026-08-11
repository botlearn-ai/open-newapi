package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// httpClient 由 main 的 InitHttpClient 赋值，测试进程里没人调用它，
// 所以每个走真实 HTTP 的测试都要先初始化一次。
var initHTTPClientOnce sync.Once

func requireHTTPClient(t *testing.T) {
	t.Helper()
	initHTTPClientOnce.Do(InitHttpClient)
}

// allowNotifications 放开 CheckNotificationLimit。
// 测试进程里 common.InitEnv 不会执行，constant.NotifyLimitCount 保持零值，
// 而 checkMemoryLimit 判的是 currentCount >= limit，所以 0 会拦掉一切通知。
func allowNotifications(t *testing.T) {
	t.Helper()
	original := constant.NotifyLimitCount
	t.Cleanup(func() { constant.NotifyLimitCount = original })
	constant.NotifyLimitCount = 1000
}

// allowLoopbackFetch 放开 SSRF 限制，让测试可以打到 httptest 的 127.0.0.1 地址。
// 生产默认 AllowPrivateIp=false 且端口白名单只有 80/443/8080/8443，httptest 用随机端口。
func allowLoopbackFetch(t *testing.T) {
	t.Helper()
	requireHTTPClient(t)
	s := system_setting.GetFetchSetting()
	original := *s
	t.Cleanup(func() { *s = original })
	s.EnableSSRFProtection = false
}

type capturedRequest struct {
	body        string
	contentType string
	userAgent   string
}

func newCaptureServer(t *testing.T, respBody string) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var got []capturedRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, capturedRequest{
			body:        string(raw),
			contentType: r.Header.Get("Content-Type"),
			userAgent:   r.Header.Get("User-Agent"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)

	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedRequest, len(got))
		copy(out, got)
		return out
	}
}

const larkSuccessBody = `{"StatusCode":0,"StatusMessage":"success","code":0,"msg":"success","data":{}}`

// 用户级通知链路：NotifyUser -> SendLarkNotify，发纯文本
func TestNotifyUserRoutesLarkToWebhook(t *testing.T) {
	allowLoopbackFetch(t)
	allowNotifications(t)
	srv, captured := newCaptureServer(t, larkSuccessBody)

	userSetting := dto.UserSetting{
		NotifyType:     dto.NotifyTypeLark,
		LarkWebhookUrl: srv.URL,
	}
	notify := dto.NewNotify(dto.NotifyTypeChannelUpdate, "通道已被禁用", "通道「x」（#1）已被禁用，原因：{{value}}", []interface{}{"401"})

	if err := NotifyUser(1, "root@example.com", userSetting, notify); err != nil {
		t.Fatalf("NotifyUser returned error: %v", err)
	}

	reqs := captured()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly 1 request to the lark webhook, got %d", len(reqs))
	}

	var payload struct {
		MsgType string `json:"msg_type"`
		Content struct {
			Text string `json:"text"`
		} `json:"content"`
		Sign string `json:"sign"`
	}
	if err := common.Unmarshal([]byte(reqs[0].body), &payload); err != nil {
		t.Fatalf("payload is not valid json: %v (%s)", err, reqs[0].body)
	}

	if payload.MsgType != "text" {
		t.Fatalf("msg_type = %q, want text", payload.MsgType)
	}
	if !strings.Contains(payload.Content.Text, "通道已被禁用") {
		t.Fatalf("title missing from text: %q", payload.Content.Text)
	}
	// {{value}} 必须被替换成实际值
	if !strings.Contains(payload.Content.Text, "401") {
		t.Fatalf("placeholder was not substituted: %q", payload.Content.Text)
	}
	if strings.Contains(payload.Content.Text, dto.ContentValueParam) {
		t.Fatalf("raw placeholder leaked into the message: %q", payload.Content.Text)
	}
	if payload.Sign != "" {
		t.Fatal("sign must be absent when no secret is configured")
	}
	if !strings.Contains(reqs[0].contentType, "application/json") {
		t.Fatalf("content-type = %q", reqs[0].contentType)
	}
}

// 没配地址时必须跳过而不是报错，与其它渠道行为一致
func TestNotifyUserSkipsLarkWhenUrlMissing(t *testing.T) {
	allowNotifications(t)
	userSetting := dto.UserSetting{NotifyType: dto.NotifyTypeLark, LarkWebhookUrl: ""}
	if err := NotifyUser(1, "root@example.com", userSetting, dto.NewNotify("t", "s", "c", nil)); err != nil {
		t.Fatalf("expected nil error when no url is configured, got %v", err)
	}
}

// 配了密钥时必须带上 timestamp + sign
func TestSendLarkNotifyIncludesSignature(t *testing.T) {
	allowLoopbackFetch(t)
	srv, captured := newCaptureServer(t, larkSuccessBody)

	err := SendLarkNotify(srv.URL, "my-secret", dto.NewNotify("t", "title", "body", nil))
	if err != nil {
		t.Fatalf("SendLarkNotify returned error: %v", err)
	}

	reqs := captured()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	var payload map[string]interface{}
	if err := common.Unmarshal([]byte(reqs[0].body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["sign"] == nil || payload["timestamp"] == nil {
		t.Fatalf("sign/timestamp missing: %s", reqs[0].body)
	}
}

// HTTP 200 + body 里 code != 0 必须判为失败 —— 这是现有通用 webhook 渠道顶不上飞书的核心原因
func TestSendLarkNotifyTreatsNonZeroCodeAsFailure(t *testing.T) {
	allowLoopbackFetch(t)
	srv, _ := newCaptureServer(t, `{"code":9499,"msg":"Bad Request","data":{}}`)

	err := SendLarkNotify(srv.URL, "", dto.NewNotify("t", "title", "body", nil))
	if err == nil {
		t.Fatal("HTTP 200 with a non-zero lark code must be reported as a failure")
	}
	if !strings.Contains(err.Error(), "9499") {
		t.Fatalf("error must surface the lark code, got: %v", err)
	}
}

// 限流错误码 11232 同样必须判为失败，否则会以为告警发出去了
func TestSendLarkNotifySurfacesRateLimitCode(t *testing.T) {
	allowLoopbackFetch(t)
	srv, _ := newCaptureServer(t, `{"code":11232,"msg":"too many request"}`)

	err := SendLarkNotify(srv.URL, "", dto.NewNotify("t", "title", "body", nil))
	if err == nil {
		t.Fatal("lark rate-limit code 11232 must be reported as a failure")
	}
	if !strings.Contains(err.Error(), "11232") {
		t.Fatalf("error must surface the rate-limit code, got: %v", err)
	}
}

// 卡片路径：告警默认走这条
func TestSendLarkCardNotifyShape(t *testing.T) {
	allowLoopbackFetch(t)
	srv, captured := newCaptureServer(t, larkSuccessBody)

	if err := SendLarkCardNotify(srv.URL, "", "标题", "**内容**", "orange"); err != nil {
		t.Fatalf("SendLarkCardNotify returned error: %v", err)
	}

	reqs := captured()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 request, got %d", len(reqs))
	}
	var payload struct {
		MsgType string `json:"msg_type"`
		Card    struct {
			Header struct {
				Template string `json:"template"`
				Title    struct {
					Content string `json:"content"`
					Tag     string `json:"tag"`
				} `json:"title"`
			} `json:"header"`
			Elements []struct {
				Tag  string `json:"tag"`
				Text struct {
					Tag     string `json:"tag"`
					Content string `json:"content"`
				} `json:"text"`
			} `json:"elements"`
		} `json:"card"`
	}
	if err := common.Unmarshal([]byte(reqs[0].body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MsgType != "interactive" {
		t.Fatalf("msg_type = %q, want interactive", payload.MsgType)
	}
	if payload.Card.Header.Template != "orange" {
		t.Fatalf("template = %q, want orange", payload.Card.Header.Template)
	}
	if payload.Card.Header.Title.Tag != "plain_text" {
		t.Fatalf("title tag = %q, want plain_text", payload.Card.Header.Title.Tag)
	}
	if len(payload.Card.Elements) != 1 || payload.Card.Elements[0].Text.Tag != "lark_md" {
		t.Fatalf("unexpected elements: %s", reqs[0].body)
	}
	if payload.Card.Elements[0].Text.Content != "**内容**" {
		t.Fatalf("body content = %q", payload.Card.Elements[0].Text.Content)
	}
}

// SSRF 防护必须对飞书 webhook 同样生效（管理员填的 URL 可能被用来探测内网）
func TestSendLarkNotifyRespectsSSRFProtection(t *testing.T) {
	requireHTTPClient(t)
	srv, captured := newCaptureServer(t, larkSuccessBody)

	s := system_setting.GetFetchSetting()
	original := *s
	t.Cleanup(func() { *s = original })
	s.EnableSSRFProtection = true
	s.AllowPrivateIp = false
	s.ApplyIPFilterForDomain = true

	err := SendLarkNotify(srv.URL, "", dto.NewNotify("t", "title", "body", nil))
	if err == nil {
		t.Fatal("a loopback webhook must be rejected while SSRF protection is on")
	}
	if !strings.Contains(err.Error(), "request reject") {
		t.Fatalf("expected an SSRF rejection, got: %v", err)
	}
	if len(captured()) != 0 {
		t.Fatal("the request must never leave the process when SSRF protection rejects it")
	}
}
