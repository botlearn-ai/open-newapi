package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/QuantumNous/new-api/migration"
)

func main() {
	if len(os.Args) != 1 || os.Getenv("VERIFY_SQL_MIGRATION_MANIFEST") != "true" {
		fmt.Fprintln(os.Stderr, "NEWAPI_SQL_MIGRATION_FAILED: fixed manifest verification required")
		os.Exit(1)
	}
	db, err := migration.Open(os.Getenv("SQL_DSN"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "NEWAPI_SQL_MIGRATION_FAILED: database connection unavailable")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	if err := migration.Run(ctx, db); err != nil {
		// Driver errors may include connection credentials or row data.
		fmt.Fprintln(os.Stderr, "NEWAPI_SQL_MIGRATION_FAILED: schema or manifest verification failed")
		os.Exit(1)
	}
	fmt.Println("NEWAPI_SQL_MIGRATION_OK: bundled manifest applied and verified")
}
