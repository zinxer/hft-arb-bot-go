package exchange

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/shopspring/decimal"
)

// MockExchange is an in-memory Exchange for tests. It never touches the network.
// Limit orders stay open until a test calls FillOrder; market orders fill
// instantly at the ticker bid (sell) or ask (buy).
type MockExchange struct {
	mu       sync.Mutex
	name     string
	tickers  map[string]Ticker
	balance  Balance
	orders   map[string]*Order
	seq      int
	Calls    []string // human-readable call log, e.g. "limit sell BTC/MYR"
	FailNext map[string]error
}

// NewMock creates an empty mock exchange.
func NewMock(name string) *MockExchange {
	return &MockExchange{
		name: name, tickers: map[string]Ticker{}, balance: Balance{},
		orders: map[string]*Order{}, FailNext: map[string]error{},
	}
}

// SetTicker sets the ticker for a symbol.
func (m *MockExchange) SetTicker(symbol string, bid, ask, last string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickers[symbol] = Ticker{
		Bid: decimal.RequireFromString(bid), Ask: decimal.RequireFromString(ask), Last: decimal.RequireFromString(last),
	}
}

// SetBalance sets the free balance of an asset.
func (m *MockExchange) SetBalance(asset, free string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balance[asset] = AssetBalance{Free: decimal.RequireFromString(free)}
}

// FillOrder marks an open limit order as (partially) filled. If filled equals the
// amount the order becomes closed; otherwise it remains open with a partial fill.
func (m *MockExchange) FillOrder(id string, filled string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o := m.orders[id]
	o.Filled = decimal.RequireFromString(filled)
	o.Average = o.Price
	if o.Filled.Equal(o.Amount) {
		o.Status = StatusClosed
	}
}

// Order returns a copy of the stored order.
func (m *MockExchange) Order(id string) Order {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.orders[id]
}

// CallLog returns a copy of the call log.
func (m *MockExchange) CallLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.Calls...)
}

func (m *MockExchange) Name() string { return m.name }

func (m *MockExchange) fail(op string) error {
	if err, ok := m.FailNext[op]; ok {
		delete(m.FailNext, op)
		return err
	}
	return nil
}

func (m *MockExchange) FetchTicker(ctx context.Context, symbol string) (Ticker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("ticker"); err != nil {
		return Ticker{}, err
	}
	t, ok := m.tickers[symbol]
	if !ok {
		return Ticker{}, fmt.Errorf("mock: no ticker for %s", symbol)
	}
	return t, nil
}

func (m *MockExchange) FetchBalance(ctx context.Context) (Balance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("balance"); err != nil {
		return nil, err
	}
	out := Balance{}
	for k, v := range m.balance {
		out[k] = v
	}
	return out, nil
}

func (m *MockExchange) CreateLimitOrder(ctx context.Context, symbol, side string, amount, price decimal.Decimal, postOnly bool) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("limit"); err != nil {
		return Order{}, err
	}
	m.seq++
	o := &Order{ID: fmt.Sprintf("%s-%d", m.name, m.seq), Symbol: symbol, Side: side, Type: "limit",
		Price: price, Amount: amount, Status: StatusOpen}
	m.orders[o.ID] = o
	m.Calls = append(m.Calls, fmt.Sprintf("limit %s %s", side, symbol))
	return *o, nil
}

func (m *MockExchange) CreateMarketOrder(ctx context.Context, symbol, side string, amount decimal.Decimal) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("market"); err != nil {
		return Order{}, err
	}
	t, ok := m.tickers[symbol]
	if !ok {
		return Order{}, fmt.Errorf("mock: no ticker for %s", symbol)
	}
	px := t.Bid
	if side == Buy {
		px = t.Ask
	}
	m.seq++
	o := &Order{ID: fmt.Sprintf("%s-%d", m.name, m.seq), Symbol: symbol, Side: side, Type: "market",
		Price: px, Amount: amount, Filled: amount, Average: px, Status: StatusClosed}
	m.orders[o.ID] = o
	m.Calls = append(m.Calls, fmt.Sprintf("market %s %s %s", side, symbol, amount))
	return *o, nil
}

func (m *MockExchange) CancelOrder(ctx context.Context, id, symbol string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("cancel"); err != nil {
		return err
	}
	o, ok := m.orders[id]
	if !ok {
		return errors.New("mock: unknown order " + id)
	}
	if o.Status == StatusOpen {
		o.Status = StatusCanceled
	}
	m.Calls = append(m.Calls, "cancel "+id)
	return nil
}

func (m *MockExchange) FetchOrder(ctx context.Context, id, symbol string) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fail("fetch"); err != nil {
		return Order{}, err
	}
	o, ok := m.orders[id]
	if !ok {
		return Order{}, errors.New("mock: unknown order " + id)
	}
	return *o, nil
}
