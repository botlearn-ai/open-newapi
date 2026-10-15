package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func IntegrationFunding(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req dto.IntegrationFundingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid funding query"})
		return
	}
	page, err := service.GetIntegrationFunding(integrationIdFromContext(c), req)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrIntegrationFundingInput) {
			status = http.StatusBadRequest
		} else if errors.Is(err, service.ErrIntegrationAccountNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"success": false, "message": "funding history unavailable"})
		return
	}
	common.ApiSuccess(c, page)
}
