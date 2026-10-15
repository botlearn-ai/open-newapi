package migration

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

type spendingLogV2 struct {
	BillingScope string `gorm:"type:varchar(36);not null;default:''"`
}

func (spendingLogV2) TableName() string { return "logs" }

const spendingVersion = "20261015_0002_course_spending"

// This immutable second manifest runs under Run's existing database lock.
func applySpending(db *gorm.DB) error {
	if !db.Migrator().HasTable("logs") {
		return fmt.Errorf("required baseline table missing: logs")
	}
	asset, err := files.ReadFile("sql/spending_" + db.Dialector.Name() + ".sql")
	if err != nil {
		return err
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(asset))
	var history []ledgerRow
	if err := db.Table(ledgerTable).Where("version = ?", spendingVersion).Find(&history).Error; err != nil {
		return err
	}
	if len(history) > 0 && history[0].Checksum != checksum {
		return fmt.Errorf("migration history does not match spending manifest")
	}
	if len(history) == 0 {
		statements := strings.Split(string(asset), ";")
		if len(statements) != 4 {
			return fmt.Errorf("invalid spending manifest")
		}
		if !db.Migrator().HasColumn(&spendingLogV2{}, "billing_scope") {
			if err := db.Exec(strings.TrimSpace(statements[0])).Error; err != nil {
				return err
			}
		}
		if err := db.Exec(strings.TrimSpace(statements[1])).Error; err != nil {
			return err
		}
		// Insert only if absent: never move the boundary on retry or rollback.
		if err := db.Exec(strings.TrimSpace(statements[2])).Error; err != nil {
			return err
		}
	}
	columns, err := db.Migrator().ColumnTypes("logs")
	if err != nil {
		return err
	}
	valid := false
	for _, c := range columns {
		if c.Name() != "billing_scope" {
			continue
		}
		kind := strings.ToLower(c.DatabaseTypeName())
		if kind != "varchar" && kind != "text" && kind != "character varying" {
			return fmt.Errorf("unexpected billing_scope type")
		}
		if nullable, ok := c.Nullable(); ok && nullable {
			return fmt.Errorf("nullable billing_scope")
		}
		if db.Dialector.Name() != "sqlite" {
			if n, ok := c.Length(); !ok || n != 36 {
				return fmt.Errorf("unexpected billing_scope length")
			}
		}
		def, ok := c.DefaultValue()
		if !ok || strings.Trim(strings.Split(def, "::")[0], "'\"()") != "" {
			return fmt.Errorf("unexpected billing_scope default")
		}
		valid = true
	}
	if !valid {
		return fmt.Errorf("billing_scope missing")
	}
	var rows []struct {
		Id        int
		LastLogId int
	}
	if err := db.Table("integration_consumption_cutovers").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].Id != 1 || rows[0].LastLogId < 0 {
		return fmt.Errorf("invalid consumption cutover")
	}
	if len(history) == 0 {
		return db.Exec("INSERT INTO "+ledgerTable+" (version, checksum, applied_at) VALUES (?, ?, ?)", spendingVersion, checksum, time.Now().Unix()).Error
	}
	return nil
}
