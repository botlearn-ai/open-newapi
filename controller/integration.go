package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func integrationIdFromContext(c *gin.Context) string {
	return c.GetString(middleware.IntegrationIdContextKey)
}

func writeIntegrationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrIntegrationAccountNotFound), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"success": false,
			"message": err.Error(),
		})
	case errors.Is(err, service.ErrIdempotencyKeyRequired):
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
	case errors.Is(err, service.ErrIdempotencyConflict),
		errors.Is(err, service.ErrIntegrationIdentityConflict),
		errors.Is(err, service.ErrIntegrationRequestConflict),
		errors.Is(err, service.ErrIntegrationRequestBudget):
		c.JSON(http.StatusConflict, gin.H{
			"success": false,
			"message": err.Error(),
		})
	default:
		common.ApiError(c, err)
	}
}

func handleIntegrationProvision(c *gin.Context, defaultInitialUsd float64) {
	var req dto.IntegrationProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	view, err := service.ProvisionIntegrationAccount(
		integrationIdFromContext(c),
		req,
		defaultInitialUsd,
	)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, view)
}

func IntegrationProvision(c *gin.Context) {
	handleIntegrationProvision(c, 0)
}

func IntegrationBalance(c *gin.Context) {
	var req dto.IntegrationProvisionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	view, err := service.GetIntegrationBalance(
		integrationIdFromContext(c),
		req.ExternalUserId,
	)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, view)
}

func IntegrationTopUp(c *gin.Context) {
	var req dto.IntegrationTopUpRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	if idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key")); idempotencyKey != "" {
		req.IdempotencyKey = idempotencyKey
	}
	view, err := service.TopUpIntegrationAccount(
		integrationIdFromContext(c),
		req,
	)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, view)
}

func IntegrationPrepareRequest(c *gin.Context) {
	var req dto.IntegrationRequestInput
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	result, err := service.PrepareIntegrationRequest(integrationIdFromContext(c), req)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func IntegrationGetRequest(c *gin.Context) {
	var req dto.IntegrationRequestInput
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	result, err := service.GetIntegrationRequest(integrationIdFromContext(c), req)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func IntegrationCancelRequest(c *gin.Context) {
	var req dto.IntegrationRequestInput
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, err)
		return
	}
	result, err := service.CancelIntegrationRequest(integrationIdFromContext(c), req)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}
