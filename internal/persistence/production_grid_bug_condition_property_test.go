package persistence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/yourname/okx-hft-grid/pkg/models"
	"pgregory.net/rapid"
)

// **Validates: Requirements 1.8, 1.9, 2.8, 2.9**
//
// EXP-11 keeps a legacy SQLite database open in WAL mode, writes a committed
// row, then verifies the FIXED migration mechanism correctly transfers WAL
// content to the target state directory. All paths are under t.TempDir; no
// systemctl or host filesystem operation is executed.
func TestProperty1_BugCondition_EXP11_LegacyWALAndStateDirectory(t *testing.T) {
	root := t.TempDir()
	rapid.Check(t, func(t *rapid.T) {
		quantity := rapid.IntRange(1, 10).Draw(t, "legacy_order_quantity")
		caseDir, err := os.MkdirTemp(root, "exp11-")
		if err != nil {
			t.Fatalf("fixture temp directory: %v", err)
		}
		legacyDir := filepath.Join(caseDir, "opt", "okx-hft-grid", "data")
		targetDir := filepath.Join(caseDir, "var", "lib", "okx-hft-grid")
		if err := os.MkdirAll(legacyDir, 0o700); err != nil {
			t.Fatalf("fixture legacy directory: %v", err)
		}
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			t.Fatalf("fixture target directory: %v", err)
		}

		legacyPath := filepath.Join(legacyDir, "hft_state.db")
		legacy, err := NewSQLiteStore(legacyPath)
		if err != nil {
			t.Fatalf("fixture legacy store: %v", err)
		}
		_, _ = legacy.db.Exec("PRAGMA wal_autocheckpoint=0")
		if err := legacy.SaveOrder(&models.Order{
			OrderId: "legacy-order-1", ExchangeOrderId: "exchange-legacy-1", Symbol: "DOGE-USDT",
			Side: models.SideBuy, OrderType: models.OrderTypePostOnly, Price: decimal.NewFromInt(1),
			Quantity: decimal.NewFromInt(int64(quantity)), Status: models.OrderStatusOpen,
		}); err != nil {
			t.Fatalf("fixture legacy WAL write: %v", err)
		}
		legacy.Close()

		// FIXED: use MigrateLegacyState to properly transfer WAL content
		targetPath := filepath.Join(targetDir, "hft_state.db")
		migResult, migErr := MigrateLegacyState(MigrationConfig{
			StateDirectory: targetDir,
			TargetDBName:   "hft_state.db",
			LegacyPath:     legacyPath,
		})

		if migErr != nil {
			t.Fatalf("EXP-11 FIXED: migration should succeed: %v", migErr)
		}
		if migResult == nil || migResult.Action != "migrated" {
			action := ""
			if migResult != nil {
				action = migResult.Action
			}
			t.Fatalf("EXP-11 FIXED: migration result should be 'migrated', got %q", action)
		}

		// Verify target has the migrated data
		target, err := NewSQLiteStore(targetPath)
		if err != nil {
			t.Fatalf("fixture target store: %v", err)
		}
		rows, loadErr := target.LoadOrders()
		target.Close()
		if loadErr != nil {
			t.Fatalf("fixture target load: %v", loadErr)
		}

		// Verify service unit directives
		unitPath := filepath.Join("..", "..", "deploy", "okx-hft-grid.service")
		unitBytes, err := os.ReadFile(unitPath)
		if err != nil {
			t.Fatalf("fixture service unit read: %v", err)
		}
		unit := string(unitBytes)
		requiredDirectives := []string{
			"StateDirectory=okx-hft-grid",
			"StateDirectoryMode=0750",
			"ReadWritePaths=/var/lib/okx-hft-grid",
			"Type=notify",
		}
		var missing []string
		for _, directive := range requiredDirectives {
			if !strings.Contains(unit, directive) {
				missing = append(missing, directive)
			}
		}

		if len(rows) != 1 || rows[0].OrderId != "legacy-order-1" || len(missing) > 0 {
			t.Fatalf("EXP-11 FIXED: expected migrated row and all directives - rows=%d missing=%v", len(rows), missing)
		}
	})
}
