package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func IntegrationRecoverCredential(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req dto.IntegrationCredentialRecoveryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid credential recovery request"})
		return
	}
	view, err := service.RecoverIntegrationCredential(integrationIdFromContext(c), req)
	if err != nil {
		// Never echo a provider/database error from a credential-bearing operation.
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "integration credential recovery rejected"})
		return
	}
	common.ApiSuccess(c, view)
}
