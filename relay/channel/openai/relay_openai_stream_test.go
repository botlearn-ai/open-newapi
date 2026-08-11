package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupOAIStreamTest(t *testing.T, body string) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo, *http.Response) {
	t.Helper()
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 30
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{
		StartTime:       time.Now(),
		IsStream:        true,
		RelayMode:       relayconstant.RelayModeChatCompletions,
		OriginModelName: "gpt-3.5-turbo",
		RelayFormat:     types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			UpstreamModelName: "gpt-3.5-turbo",
			ChannelSetting:    dto.ChannelSettings{},
		},
	}
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	return c, recorder, info, resp
}

func TestOaiStreamHandler_IncompleteBeforeCommitReturnsRetryableError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-3.5-turbo\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n"
	c, recorder, info, resp := setupOAIStreamTest(t, body)

	usage, apiErr := OaiStreamHandler(c, info, resp)

	assert.Nil(t, usage)
	require.NotNil(t, apiErr)
	assert.Equal(t, http.StatusBadGateway, apiErr.StatusCode)
	assert.Equal(t, types.ErrorCodeUpstreamStreamIncomplete, apiErr.GetErrorCode())
	assert.Empty(t, recorder.Body.String())
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonIncomplete, info.StreamStatus.EndReason)
	assert.Equal(t, 0, info.SendResponseCount)
}

func TestOaiStreamHandler_IncompleteAfterCommitDoesNotFakeDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"part-1"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"part-2"},"finish_reason":null}]}`,
	}, "\n") + "\n"
	c, recorder, info, resp := setupOAIStreamTest(t, body)

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	output := recorder.Body.String()
	assert.Contains(t, output, "part-1")
	assert.Contains(t, output, "part-2")
	assert.NotContains(t, output, "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonIncomplete, info.StreamStatus.EndReason)
	assert.Greater(t, info.SendResponseCount, 0)
}

func TestOaiStreamHandler_FinishReasonAllowsEOFWithoutDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"complete"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}, "\n") + "\n"
	c, recorder, info, resp := setupOAIStreamTest(t, body)

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonEOF, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasTerminal())
}

func TestOaiStreamHandler_DoneRemainsSuccessful(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"complete"},"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"data: [DONE]",
	}, "\n") + "\n"
	c, recorder, info, resp := setupOAIStreamTest(t, body)

	usage, apiErr := OaiStreamHandler(c, info, resp)

	require.Nil(t, apiErr)
	require.NotNil(t, usage)
	assert.Contains(t, recorder.Body.String(), "[DONE]")
	require.NotNil(t, info.StreamStatus)
	assert.Equal(t, relaycommon.StreamEndReasonDone, info.StreamStatus.EndReason)
	assert.True(t, info.StreamStatus.HasTerminal())
}

func TestIsOpenAIStreamTerminal(t *testing.T) {
	tests := []struct {
		name      string
		relayMode int
		data      string
		want      bool
	}{
		{
			name:      "chat finish reason",
			relayMode: relayconstant.RelayModeChatCompletions,
			data:      `{"choices":[{"finish_reason":null},{"finish_reason":"stop"}]}`,
			want:      true,
		},
		{
			name:      "chat partial",
			relayMode: relayconstant.RelayModeChatCompletions,
			data:      `{"choices":[{"finish_reason":null}]}`,
			want:      false,
		},
		{
			name:      "legacy completion finish reason",
			relayMode: relayconstant.RelayModeCompletions,
			data:      `{"choices":[{"finish_reason":"length"}]}`,
			want:      true,
		},
		{
			name:      "malformed data",
			relayMode: relayconstant.RelayModeChatCompletions,
			data:      `{`,
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isOpenAIStreamTerminal(tt.relayMode, tt.data))
		})
	}
}
