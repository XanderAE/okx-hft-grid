package strategy

import (
	"errors"

	"github.com/yourname/okx-hft-grid/pkg/models"
)

// mockOrderPlacer is a test double for the OrderPlacer interface.
type mockOrderPlacer struct {
	failCount int // number of times to fail before succeeding
	calls     int
}

func (m *mockOrderPlacer) PlaceOrder(order *models.Order) error {
	m.calls++
	if m.calls <= m.failCount {
		return errors.New("placement failed")
	}
	return nil
}
