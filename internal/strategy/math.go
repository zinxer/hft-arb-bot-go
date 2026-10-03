// Package strategy holds the pure arbitrage math and the order lifecycle.
package strategy

import (
	"github.com/shopspring/decimal"

	"github.com/zinxer/hft-arb-bot-go/internal/exchange"
)

var hundred = decimal.NewFromInt(100)

// UsdtToMyr converts a USDT amount to MYR, rounded to 2 dp (as the Node bot does).
func UsdtToMyr(usdt, rate decimal.Decimal) decimal.Decimal {
	return usdt.Mul(rate).Round(2)
}

// GapPercent is (masterLast - slaveBidMYR) / masterLast * 100.
// Positive means the master is priced above the slave. A zero masterLast yields zero.
func GapPercent(masterLast, slaveBidUSDT, rate decimal.Decimal) decimal.Decimal {
	if masterLast.IsZero() {
		return decimal.Zero
	}
	slaveMYR := UsdtToMyr(slaveBidUSDT, rate)
	return masterLast.Sub(slaveMYR).Div(masterLast).Mul(hundred)
}

// SlaveQuote is one slave exchange's best bid in USDT.
type SlaveQuote struct {
	Name string
	Bid  decimal.Decimal
}

// ChooseSlave returns the slave with the largest absolute gap. Ties keep the
// earlier slave (strict comparison, as in the Node bot). ok is false when no
// slave has a non-zero gap.
func ChooseSlave(masterLast, rate decimal.Decimal, slaves []SlaveQuote) (name string, gap decimal.Decimal, ok bool) {
	largest := decimal.Zero
	for _, s := range slaves {
		g := GapPercent(masterLast, s.Bid, rate)
		if largest.Abs().LessThan(g.Abs()) {
			largest, name, ok = g, s.Name, true
		}
	}
	return name, largest, ok
}

// MeetsThreshold reports whether |gap| >= safeGap.
func MeetsThreshold(gap, safeGap decimal.Decimal) bool {
	return gap.Abs().GreaterThanOrEqual(safeGap)
}

// MasterSide maps the gap sign to the master limit-order side: a master priced
// above the slave means sell on the master, below means buy. Zero returns "".
func MasterSide(gap decimal.Decimal) string {
	switch {
	case gap.IsPositive():
		return exchange.Sell
	case gap.IsNegative():
		return exchange.Buy
	}
	return ""
}

// HedgeSide is the opposite side, placed on the slave after a master fill.
func HedgeSide(masterSide string) string {
	if masterSide == exchange.Sell {
		return exchange.Buy
	}
	return exchange.Sell
}

// LimitPrice rests the order slightly beyond the best price: a sell sits at
// ask*(1-offset), a buy at bid*(1+offset); rounded to 2 dp.
func LimitPrice(side string, bid, ask, offset decimal.Decimal) decimal.Decimal {
	if side == exchange.Sell {
		return ask.Sub(offset.Mul(ask)).Round(2)
	}
	return bid.Add(offset.Mul(bid)).Round(2)
}

// OrderAmount is orderSizeMYR / price rounded to the asset's precision.
func OrderAmount(orderSizeMYR, price decimal.Decimal, places int32) decimal.Decimal {
	if price.IsZero() {
		return decimal.Zero
	}
	return orderSizeMYR.Div(price).Round(places)
}

// Profit in MYR of a hedged round trip. masterSide is the side taken on the
// master; amount is the hedged base amount.
func Profit(masterSide string, masterPrice, slaveAvgMYR, amount decimal.Decimal) decimal.Decimal {
	if masterSide == exchange.Buy {
		return slaveAvgMYR.Sub(masterPrice).Mul(amount)
	}
	return masterPrice.Sub(slaveAvgMYR).Mul(amount)
}

// Balances is the data needed for the pre-trade balance checks.
type Balances struct {
	MasterAssetFree decimal.Decimal // master free base asset
	MasterQuoteFree decimal.Decimal // master free MYR
	SlaveAssetFree  decimal.Decimal // chosen slave free base asset
	SlaveQuoteFree  decimal.Decimal // chosen slave free USDT
	MasterLast      decimal.Decimal // master last price (MYR)
	SlaveLast       decimal.Decimal // chosen slave last price (USDT)
}

// BalancesSufficient replicates the Node checks against ORDER_SIZE_MYR.
// Sell on master: master asset value >= size, and slave USDT (in MYR) >= size*numAssets.
// Buy on master: master MYR >= size*numAssets, and slave asset value (in MYR) >= size.
// reason is a short human-readable explanation when false.
func BalancesSufficient(side string, b Balances, rate, orderSize decimal.Decimal, numAssets int) (ok bool, reason string) {
	n := decimal.NewFromInt(int64(numAssets))
	if side == exchange.Sell {
		if b.MasterLast.Mul(b.MasterAssetFree).Round(2).LessThan(orderSize) {
			return false, "master asset balance below order size"
		}
		if UsdtToMyr(b.SlaveQuoteFree, rate).LessThan(orderSize.Mul(n)) {
			return false, "slave quote balance below order size for all assets"
		}
		return true, ""
	}
	if b.MasterQuoteFree.LessThan(orderSize.Mul(n)) {
		return false, "master quote balance below order size for all assets"
	}
	if UsdtToMyr(b.SlaveLast.Mul(b.SlaveAssetFree), rate).LessThan(orderSize) {
		return false, "slave asset balance below order size"
	}
	return true, ""
}
