package publicapi

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	"yunshu.dev/core/internal/control/ctltest"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	db, err := ctltest.SetupDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "publicapi tests: %v\n", err)
		os.Exit(1)
	}
	testDB = db
	code := m.Run()
	_ = db.Close()
	os.Exit(code)
}
