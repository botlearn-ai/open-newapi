package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

// postNotifyJSON 向通知目标 POST 一个 JSON 负载，并返回响应体。
//
// 这段 Worker/SSRF 分支逻辑在 sendBarkNotify / sendGotifyNotify / SendWebhookNotify
// 里各自复制了一份；此处抽出来供新增渠道复用，避免继续增加副本。
// 现有三个渠道暂未迁移，属独立重构。
//
// 与旧代码的一个重要差异：本函数会读回响应体并返回，因为部分服务端
// （飞书就是典型）用 HTTP 200 + body 里的错误码表示失败，只看状态码会误判成功。
func postNotifyJSON(url string, userAgent string, headers map[string]string, body []byte) ([]byte, error) {
	reqHeaders := map[string]string{
		"Content-Type": "application/json; charset=utf-8",
		"User-Agent":   userAgent,
	}
	for k, v := range headers {
		reqHeaders[k] = v
	}

	var resp *http.Response
	var err error

	if system_setting.EnableWorker() {
		workerReq := &WorkerRequest{
			URL:     url,
			Key:     system_setting.WorkerValidKey,
			Method:  http.MethodPost,
			Headers: reqHeaders,
			Body:    body,
		}
		resp, err = DoWorkerRequest(workerReq)
		if err != nil {
			return nil, fmt.Errorf("failed to send request through worker: %v", err)
		}
	} else {
		// SSRF 防护（非 Worker 模式）
		fetchSetting := system_setting.GetFetchSetting()
		if err = common.ValidateURLWithFetchSetting(url, fetchSetting.EnableSSRFProtection, fetchSetting.AllowPrivateIp, fetchSetting.DomainFilterMode, fetchSetting.IpFilterMode, fetchSetting.DomainList, fetchSetting.IpList, fetchSetting.AllowedPorts, fetchSetting.ApplyIPFilterForDomain); err != nil {
			return nil, fmt.Errorf("request reject: %v", err)
		}

		var req *http.Request
		req, err = http.NewRequest(http.MethodPost, url, bytes.NewBuffer(body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %v", err)
		}
		for k, v := range reqHeaders {
			req.Header.Set(k, v)
		}

		// httpClient 由 main 启动时的 InitHttpClient 赋值。这里显式判空是因为
		// 告警发送协程是长生命周期的，一次 nil 解引用会永久杀掉它。
		client := GetHttpClient()
		if client == nil {
			return nil, fmt.Errorf("http client is not initialized")
		}

		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to send request: %v", err)
		}
	}
	defer resp.Body.Close()

	// 限制读取长度，避免异常响应把内存吃掉
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return respBody, fmt.Errorf("request failed with status code %d: %s", resp.StatusCode, common.LocalLogPreview(string(respBody)))
	}
	if readErr != nil {
		return nil, fmt.Errorf("failed to read response body: %v", readErr)
	}
	return respBody, nil
}
