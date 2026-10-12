package model

// IntegrationRequest is the durable billing record for one partner-authorized
// inference attempt. It lives with wallet balances, independently of optional logs.
type IntegrationRequest struct {
	Id                int     `json:"id"`
	AccountId         int     `json:"-" gorm:"not null;uniqueIndex:idx_integration_request_key,priority:1"`
	RequestKey        string  `json:"request_key" gorm:"type:varchar(128);not null;uniqueIndex:idx_integration_request_key,priority:2"`
	Path              string  `json:"path" gorm:"type:varchar(64);not null"`
	BodySHA256        string  `json:"-" gorm:"type:char(64);not null"`
	MaxQuota          int     `json:"max_quota" gorm:"not null"`
	QuotaPerUsd       float64 `json:"quota_per_usd" gorm:"not null"`
	State             string  `json:"state" gorm:"type:varchar(16);not null;index"`
	ProviderRequestId string  `json:"provider_request_id" gorm:"type:varchar(64);not null"`
	ActualQuota       int     `json:"actual_quota" gorm:"not null;default:0"`
	ChargedQuota      int     `json:"charged_quota" gorm:"not null;default:0"`
	CreatedTime       int64   `json:"created_time" gorm:"bigint;not null"`
	SettledTime       int64   `json:"settled_time" gorm:"bigint;not null;default:0"`
}
