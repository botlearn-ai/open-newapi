package middleware

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

const IntegrationIdContextKey = "integration_id"

func integrationBearerToken(c *gin.Context) (string, error) {
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", errors.New("invalid integration authorization")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", errors.New("invalid integration authorization")
	}
	return token, nil
}

func configuredIntegrationSecret(integrationId string) (string, error) {
	raw := strings.TrimSpace(common.GetEnvOrDefaultString("INTEGRATION_CLIENTS", ""))
	if raw == "" {
		return "", errors.New("INTEGRATION_CLIENTS is not configured")
	}
	clients := map[string]string{}
	if err := common.UnmarshalJsonStr(raw, &clients); err != nil {
		return "", errors.New("INTEGRATION_CLIENTS is invalid")
	}
	secret := strings.TrimSpace(clients[integrationId])
	if secret == "" {
		return "", errors.New("integration is not configured")
	}
	return secret, nil
}

func IntegrationAuth() func(c *gin.Context) {
	failureLimit := integrationAuthFailureLimit()
	return func(c *gin.Context) {
		integrationId, err := service.NormalizeIntegrationId(c.GetHeader("X-Integration-ID"))
		if err != nil {
			if failureLimit(c); c.IsAborted() {
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "invalid integration credentials",
			})
			c.Abort()
			return
		}
		expected, err := configuredIntegrationSecret(integrationId)
		if err != nil {
			status := http.StatusUnauthorized
			if strings.Contains(err.Error(), "INTEGRATION_CLIENTS") {
				status = http.StatusServiceUnavailable
			} else if failureLimit(c); c.IsAborted() {
				return
			}
			c.JSON(status, gin.H{
				"success": false,
				"message": err.Error(),
			})
			c.Abort()
			return
		}
		provided, err := integrationBearerToken(c)
		if err != nil || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			if failureLimit(c); c.IsAborted() {
				return
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "invalid integration credentials",
			})
			c.Abort()
			return
		}
		c.Set(IntegrationIdContextKey, integrationId)
		c.Next()
	}
}
