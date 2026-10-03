package exchange

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
)

func TestMockLifecycle(t *testing.T) {
	ctx := context.Background()
	var _ Exchange = NewMock("m")
	m := NewMock("m")
	m.SetTicker("BTC/MYR", "100", "101", "100.5")
	tk, err := m.FetchTicker(ctx, "BTC/MYR")
	if err != nil || !tk.Last.Equal(decimal.RequireFromString("100.5")) {
		t.Fatalf("ticker: %v %v", tk, err)
	}
	o, _ := m.CreateLimitOrder(ctx, "BTC/MYR", Sell, decimal.NewFromInt(1), decimal.NewFromInt(101), true)
	if o.Status != StatusOpen {
		t.Fatal("want open")
	}
	m.FillOrder(o.ID, "1")
	if got, _ := m.FetchOrder(ctx, o.ID, ""); got.Status != StatusClosed {
		t.Fatal("want closed")
	}
	o2, _ := m.CreateLimitOrder(ctx, "BTC/MYR", Buy, decimal.NewFromInt(2), decimal.NewFromInt(100), false)
	_ = m.CancelOrder(ctx, o2.ID, "")
	if m.Order(o2.ID).Status != StatusCanceled {
		t.Fatal("want canceled")
	}
	mo, _ := m.CreateMarketOrder(ctx, "BTC/MYR", Buy, decimal.NewFromInt(1))
	if !mo.Average.Equal(decimal.NewFromInt(101)) {
		t.Fatalf("market buy fills at ask, got %s", mo.Average)
	}
}

func TestSplitSymbol(t *testing.T) {
	if b, q, err := SplitSymbol("ETH/USDT"); err != nil || b != "ETH" || q != "USDT" {
		t.Fatal("split")
	}
	if _, _, err := SplitSymbol("ETH"); err == nil {
		t.Fatal("want error")
	}
}
