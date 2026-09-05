package model

// IntegrationAccount maps one third-party service user to a new-api user and
// the API token managed by that service.
type IntegrationAccount struct {
	Id              int    `json:"id"`
	IntegrationId   string `json:"integration_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_integration_external,priority:1"`
	ExternalUserKey string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_integration_external,priority:2"`
	ExternalUserId  string `json:"external_user_id" gorm:"type:text;not null"`
	UserId          int    `json:"user_id" gorm:"not null;index"`
	TokenId         int    `json:"token_id" gorm:"not null;index"`
	CreatedTime     int64  `json:"created_time" gorm:"bigint;not null"`
}

// IntegrationOperation makes credit mutations idempotent for each third-party
// service. Idempotency keys are hashed to keep the unique index compact and
// compatible with SQLite, MySQL, and PostgreSQL.
type IntegrationOperation struct {
	AdminId            int    `json:"admin_id,omitempty" gorm:"not null;default:0"`
	Id                 int    `json:"id"`
	IntegrationId      string `json:"integration_id" gorm:"type:varchar(64);not null;uniqueIndex:idx_integration_idempotency,priority:1"`
	IdempotencyKeyHash string `json:"-" gorm:"type:char(64);not null;uniqueIndex:idx_integration_idempotency,priority:2"`
	RequestHash        string `json:"-" gorm:"type:char(64);not null"`
	AccountId          int    `json:"account_id" gorm:"not null;index"`
	Operation          string `json:"operation" gorm:"type:varchar(32);not null"`
	Quota              int    `json:"quota" gorm:"not null"`
	CreatedTime        int64  `json:"created_time" gorm:"bigint;not null"`
}
