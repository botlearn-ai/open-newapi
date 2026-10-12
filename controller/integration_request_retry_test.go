package controller

import (
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRequestBoundedRequestDoesNotRetryUnknownExecution(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Request.Header.Set(service.IntegrationRequestHeader, "one-authorized-attempt")
	failure := types.NewErrorWithStatusCode(errors.New("upstream returned 502 after execution became uncertain"), types.ErrorCodeDoRequestFailed, 502)
	require.False(t, shouldRetry(c, failure, 1), "another upstream execution must require another recorded attempt")
	c.Request.Header.Del(service.IntegrationRequestHeader)
	require.True(t, shouldRetry(c, failure, 1), "ordinary accounts retain their existing retry policy")
}
