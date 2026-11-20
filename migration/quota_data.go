package migration

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const quotaDataVersion = "20261120_0004_quota_data_bucket"
const quotaDataTable = "quota_data"
const quotaDataIndex = "idx_quota_data_bucket"
const quotaDataColumns = "user_id,username,model_name,created_at"
const quotaDataInvalidRows = `SELECT EXISTS (
	SELECT 1 FROM quota_data WHERE user_id IS NULL OR username IS NULL OR model_name IS NULL OR created_at IS NULL
	OR count IS NULL OR quota IS NULL OR token_used IS NULL
) OR EXISTS (
	SELECT 1 FROM quota_data GROUP BY ` + quotaDataColumns + ` HAVING COUNT(*) > 1
)`
const quotaDataCreateIndex = "CREATE UNIQUE INDEX " + quotaDataIndex + " ON " + quotaDataTable + " (" + quotaDataColumns + ")"

// ErrQuotaDataNeedsReview reports legacy buckets requiring evidence before repair.
var ErrQuotaDataNeedsReview = errors.New("quota data has duplicate or incomplete buckets; reconcile against consumption logs before retrying")

func applyQuotaData(db *gorm.DB) error {
	if !db.Migrator().HasTable(quotaDataTable) {
		return errors.New("required baseline table missing: quota_data")
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte(quotaDataInvalidRows+";"+quotaDataCreateIndex+";")))
	var history []ledgerRow
	if err := db.Table(ledgerTable).Where("version = ?", quotaDataVersion).Find(&history).Error; err != nil {
		return err
	}
	if len(history) > 0 && history[0].Checksum != checksum {
		return errors.New("migration history does not match quota data manifest")
	}
	if len(history) == 0 {
		var invalid bool
		if err := db.Raw(quotaDataInvalidRows).Scan(&invalid).Error; err != nil {
			return err
		}
		if invalid {
			return ErrQuotaDataNeedsReview
		}
		if !db.Migrator().HasIndex(quotaDataTable, quotaDataIndex) {
			if err := db.Exec(quotaDataCreateIndex).Error; err != nil {
				return err
			}
		}
	}
	if err := verifyQuotaDataConflictIndex(db); err != nil {
		return err
	}
	if len(history) == 0 {
		return db.Exec("INSERT INTO "+ledgerTable+" (version, checksum, applied_at) VALUES (?, ?, ?)", quotaDataVersion, checksum, time.Now().Unix()).Error
	}
	return nil
}

// A partial, invalid or prefix index can have the expected name and columns
// without enforcing the conflict target over every complete bucket.
func verifyQuotaDataConflictIndex(db *gorm.DB) error {
	dialect := db.Dialector.Name()
	// The PostgreSQL GORM driver aggregates index columns without ordering them.
	if dialect != "postgres" {
		if err := verifyTableIndexes(db, quotaDataTable, []indexSpec{{quotaDataIndex, quotaDataColumns, true}}); err != nil {
			return err
		}
	}
	var query string
	args := []any{quotaDataIndex}
	switch dialect {
	case "postgres":
		query = `SELECT EXISTS (
			SELECT 1 FROM pg_index
			WHERE indexrelid = to_regclass(?) AND indrelid = to_regclass('quota_data')
				AND indisunique AND indisvalid AND indisready AND indimmediate AND indpred IS NULL AND indnatts = 4
				AND (SELECT string_agg(pg_get_indexdef(indexrelid, ord, true), ',' ORDER BY ord)
					FROM generate_series(1, 4) AS ord) = ?
		)`
		args = append(args, quotaDataColumns)
	case "sqlite":
		query = `SELECT EXISTS (SELECT 1 FROM pragma_index_list('quota_data') WHERE name = ? AND partial = 0)`
	case "mysql":
		query = `SELECT COUNT(*) = 4 FROM information_schema.statistics
			WHERE table_schema = DATABASE() AND table_name = 'quota_data' AND index_name = ? AND sub_part IS NULL`
	default:
		return errors.New("unsupported migration dialect")
	}
	var valid bool
	if err := db.Raw(query, args...).Scan(&valid).Error; err != nil {
		return err
	}
	if !valid {
		return errors.New("required quota data conflict index shape mismatch")
	}
	return nil
}
