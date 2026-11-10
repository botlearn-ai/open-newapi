package migration

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const reconciliationVersion = "20261110_0003_request_reconciliation"
const reconciliationTable = "integration_request_reconciliations"
const reconciliationIndex = "idx_integration_request_reconciliations_request_id"

// Frozen V3 shape; existing manifests and their checksums remain unchanged.
type requestReconciliationV3 struct {
	Id                int    `gorm:"primaryKey;autoIncrement"`
	RequestId         int    `gorm:"not null"`
	AdminId           int    `gorm:"not null"`
	ProviderRequestId string `gorm:"type:varchar(64);not null"`
	ActualQuota       int    `gorm:"not null"`
	EvidenceRef       string `gorm:"type:text;not null"`
	Reason            string `gorm:"type:text;not null"`
	CreatedTime       int64  `gorm:"bigint;not null"`
}

func (requestReconciliationV3) TableName() string { return reconciliationTable }

func reconciliationManifest(db *gorm.DB) (string, error) {
	trace := &schemaLogger{Interface: logger.Default.LogMode(logger.Silent)}
	if err := db.Session(&gorm.Session{DryRun: true, Logger: trace}).Migrator().CreateTable(&requestReconciliationV3{}); err != nil {
		return "", err
	}
	if len(trace.statements) != 1 {
		return "", fmt.Errorf("unexpected reconciliation table manifest")
	}
	return trace.statements[0] + ";CREATE UNIQUE INDEX " + reconciliationIndex + " ON " + reconciliationTable + "(request_id);", nil
}

// Run owns the transaction/connection lock, including resumable MySQL DDL.
func applyReconciliation(db *gorm.DB) error {
	manifest, err := reconciliationManifest(db)
	if err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(manifest)))
	var history []ledgerRow
	if err := db.Table(ledgerTable).Where("version = ?", reconciliationVersion).Find(&history).Error; err != nil {
		return err
	}
	if len(history) > 0 && history[0].Checksum != checksum {
		return fmt.Errorf("migration history does not match reconciliation manifest")
	}
	if len(history) == 0 {
		statements := strings.Split(manifest, ";")
		for i, present := range []bool{
			db.Migrator().HasTable(reconciliationTable),
			db.Migrator().HasIndex(reconciliationTable, reconciliationIndex),
		} {
			if !present {
				if err := db.Exec(statements[i]).Error; err != nil {
					return err
				}
			}
		}
	}
	if err := verifyColumns(db, map[string]map[string]columnSpec{
		reconciliationTable: {
			"id": {kind: "integer"}, "request_id": {kind: "integer"},
			"admin_id": {kind: "integer"}, "actual_quota": {kind: "integer"},
			"provider_request_id": {kind: "string", length: 64},
			"evidence_ref":        {kind: "string"}, "reason": {kind: "string"},
			"created_time": {kind: "integer"},
		},
	}); err != nil {
		return err
	}
	if err := verifyTableIndexes(db, reconciliationTable, []indexSpec{{reconciliationIndex, "request_id", true}}); err != nil {
		return err
	}
	if len(history) == 0 {
		return db.Exec("INSERT INTO "+ledgerTable+" (version, checksum, applied_at) VALUES (?, ?, ?)", reconciliationVersion, checksum, time.Now().Unix()).Error
	}
	return nil
}
