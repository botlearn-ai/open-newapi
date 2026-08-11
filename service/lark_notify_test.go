package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
)

// 飞书的签名规则与钉钉相反：待签串 "{timestamp}\n{secret}" 作为 HMAC 的 KEY，
// 消息体为空。写反了签名校验会永远失败，所以这里把两种写法都算出来并断言不相等，
// 确保实现用的是飞书那一种。
func TestLarkSignUsesStringToSignAsKey(t *testing.T) {
	const secret = "test-secret"
	const timestamp int64 = 1599360473

	got := larkSign(timestamp, secret)

	stringToSign := "1599360473\n" + secret
	feishuStyle := hmac.New(sha256.New, []byte(stringToSign))
	feishuStyle.Write([]byte(""))
	want := base64.StdEncoding.EncodeToString(feishuStyle.Sum(nil))

	if got != want {
		t.Fatalf("larkSign = %q, want %q", got, want)
	}

	// 钉钉写法（key=secret，msg=待签串）必须与飞书写法不同，否则这个测试没有区分力
	dingStyle := hmac.New(sha256.New, []byte(secret))
	dingStyle.Write([]byte(stringToSign))
	dingSign := base64.StdEncoding.EncodeToString(dingStyle.Sum(nil))
	if got == dingSign {
		t.Fatal("feishu and dingtalk signatures collided; test cannot detect the swap")
	}
}

func TestLarkResponseOk(t *testing.T) {
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		// 实际抓到的飞书成功响应同时带两套字段
		{"both field sets success", `{"StatusCode":0,"StatusMessage":"success","code":0,"data":{},"msg":"success"}`, true},
		{"new fields only", `{"code":0,"msg":"success","data":{}}`, true},
		{"new fields error", `{"code":9499,"msg":"Bad Request","data":{}}`, false},
		{"legacy fields error", `{"StatusCode":9499,"StatusMessage":"Bad Request"}`, false},
		// 限流错误码，必须判为失败
		{"rate limited", `{"code":11232,"msg":"rate limited"}`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp larkResponse
			if err := common.Unmarshal([]byte(tc.body), &resp); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}
			if resp.ok() != tc.ok {
				t.Fatalf("ok() = %v, want %v (body %s)", resp.ok(), tc.ok, tc.body)
			}
			if !tc.ok && resp.errMsg() == "" {
				t.Fatal("errMsg() must not be empty for a failure response")
			}
		})
	}
}

// 占位符必须用 {{value}} 逐个替换，而不是 service/webhook.go 里的
// fmt.Sprintf(content, value) —— 后者在模板不含 %v 时会静默丢值。
func TestApplyNotifyValues(t *testing.T) {
	got := applyNotifyValues(
		"first {{value}} then {{value}}",
		[]interface{}{"a", 42},
	)
	if got != "first a then 42" {
		t.Fatalf("applyNotifyValues = %q", got)
	}

	// 模板里没有 %v 时，值不应被丢弃
	got = applyNotifyValues("quota left: {{value}}", []interface{}{100})
	if !strings.Contains(got, "100") {
		t.Fatalf("value was dropped: %q", got)
	}
}

func TestBuildLarkPayloadSignPresence(t *testing.T) {
	withoutSecret, err := buildLarkTextPayload("t", "c", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := withoutSecret["sign"]; ok {
		t.Fatal("sign must be omitted when no secret is configured")
	}
	if _, ok := withoutSecret["timestamp"]; ok {
		t.Fatal("timestamp must be omitted when no secret is configured")
	}

	withSecret, err := buildLarkTextPayload("t", "c", "s")
	if err != nil {
		t.Fatal(err)
	}
	if withSecret["sign"] == nil || withSecret["timestamp"] == nil {
		t.Fatal("sign and timestamp must be present when a secret is configured")
	}
	if withSecret["msg_type"] != "text" {
		t.Fatalf("msg_type = %v, want text", withSecret["msg_type"])
	}

	card, err := buildLarkCardPayload("t", "c", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if card["msg_type"] != "interactive" {
		t.Fatalf("msg_type = %v, want interactive", card["msg_type"])
	}
	// 空 template 要回落到 red，而不是发出一个飞书不认的空字符串
	cardBody, ok := card["card"].(map[string]interface{})
	if !ok {
		t.Fatal("card payload missing card object")
	}
	header, ok := cardBody["header"].(map[string]interface{})
	if !ok {
		t.Fatal("card payload missing header")
	}
	if header["template"] != "red" {
		t.Fatalf("template = %v, want red fallback", header["template"])
	}
}

// 负载必须能被 common.Marshal 序列化（CLAUDE.md Rule 1 要求走这个包装器）
func TestLarkPayloadIsMarshalable(t *testing.T) {
	payload, err := buildLarkCardPayload("title", "**bold**", "orange", "secret")
	if err != nil {
		t.Fatal(err)
	}
	body, err := common.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(body), `"msg_type":"interactive"`) {
		t.Fatalf("unexpected payload: %s", body)
	}
}

func TestDtoNotifyForAlertUsesFailureType(t *testing.T) {
	n := dtoNotifyForAlert("title", "content")
	if n.Type != dto.NotifyTypeLLMFailure {
		t.Fatalf("Type = %q, want %q", n.Type, dto.NotifyTypeLLMFailure)
	}
	if len(n.Values) != 0 {
		t.Fatal("Values must be empty: content is already fully rendered")
	}
}
