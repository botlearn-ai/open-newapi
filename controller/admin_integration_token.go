package controller

import (
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func adminIntegrationTarget(c *gin.Context) (*model.User, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return nil, false
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		common.ApiErrorI18n(c, i18n.MsgUserNotExists)
		return nil, false
	}
	if c.GetInt("role") < common.RoleAdminUser || !canManageTargetRole(c.GetInt("role"), user.Role) {
		common.ApiErrorI18n(c, i18n.MsgUserNoPermissionHigherLevel)
		return nil, false
	}
	return user, true
}

func AdminGetIntegrationTokens(c *gin.Context) {
	user, ok := adminIntegrationTarget(c)
	if !ok {
		return
	}
	tokens, err := service.GetAdminIntegrationTokens(user.Id)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"user_quota": user.Quota, "tokens": tokens})
}

func AdminAddIntegrationTokenQuota(c *gin.Context) {
	user, ok := adminIntegrationTarget(c)
	if !ok {
		return
	}
	tokenId, err := strconv.Atoi(c.Param("token_id"))
	if err != nil || tokenId <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	var req struct {
		Quota int `json:"quota"`
	}
	if err := common.DecodeJson(c.Request.Body, &req); err != nil || req.Quota <= 0 {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	replayed, err := service.AddAdminIntegrationTokenQuota(c.GetInt("id"), user.Id, tokenId, req.Quota, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeIntegrationError(c, err)
		return
	}
	if !replayed {
		model.RecordLogWithAdminInfo(user.Id, model.LogTypeManage, fmt.Sprintf("管理员增加集成令牌 #%d 额度 %s", tokenId, logger.LogQuota(req.Quota)), map[string]interface{}{"admin_id": c.GetInt("id"), "admin_username": c.GetString("username"), "token_id": tokenId, "quota": req.Quota})
	}
	common.ApiSuccess(c, gin.H{"replayed": replayed})
}
