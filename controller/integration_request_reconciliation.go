package controller

import (
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func AdminListIntegrationRequests(c *gin.Context) {
	user, ok := adminIntegrationTarget(c)
	if !ok {
		return
	}
	before, err := strconv.Atoi(c.DefaultQuery("before", "0"))
	if err != nil || before < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid cursor"})
		return
	}
	rows, err := service.ListAdminIntegrationRequests(user.Id, before)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, rows)
}

func AdminReconcileIntegrationRequest(c *gin.Context) {
	if c.GetInt("role") != common.RoleRootUser || c.GetBool("use_access_token") {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "root browser session required"})
		return
	}
	user, ok := adminIntegrationTarget(c)
	if !ok {
		return
	}
	id, err := strconv.Atoi(c.Param("request_id"))
	var input dto.IntegrationRequestReconciliationInput
	if err != nil || id <= 0 || common.DecodeJson(c.Request.Body, &input) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid reconciliation request"})
		return
	}
	result, err := service.ReconcileIntegrationRequest(c.GetInt("id"), user.Id, id, input)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, result)
}

func AdminGetIntegrationRequest(c *gin.Context) {
	user, ok := adminIntegrationTarget(c)
	if !ok {
		return
	}
	id, err := strconv.Atoi(c.Param("request_id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid request id"})
		return
	}
	detail, err := service.GetAdminIntegrationRequest(user.Id, id)
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	common.ApiSuccess(c, detail)
}
