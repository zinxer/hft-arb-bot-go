package strategy

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestGapPercent(t *testing.T) {
	// slave bid 20000 USDT * 4.5 = 90000 MYR; master 100000 -> +10%
	if g := GapPercent(d("100000"), d("20000"), d("4.5")); !g.Equal(d("10")) {
		t.Fatalf("got %s", g)
	}
	// master below slave -> negative
	if g := GapPercent(d("80000"), d("20000"), d("4.5")); !g.IsNegative() {
		t.Fatalf("got %s", g)
	}
	if g := GapPercent(decimal.Zero, d("1"), d("4")); !g.IsZero() {
		t.Fatal("zero master last must not divide by zero")
	}
}

func TestChooseSlave(t *testing.T) {
	slaves := []SlaveQuote{{"A", d("20000")}, {"B", d("21000")}, {"C", d("19000")}}
	// master 90000 MYR, rate 4.5: A gap 0, B (94500 MYR) gap -5, C (85500 MYR) gap +5
	name, gap, ok := ChooseSlave(d("90000"), d("4.5"), slaves)
	if !ok || name != "B" || !gap.Equal(d("-5")) {
		t.Fatalf("got %s %s %v ", name, gap, ok)
	}
	if _, _, ok := ChooseSlave(d("90000"), d("4.5"), []SlaveQuote{{"A", d("20000")}}); ok {
		t.Fatal("zero gap should not select a slave")
	}
	if _, _, ok := ChooseSlave(d("90000"), d("4.5"), nil); ok {
		t.Fatal("no slaves")
	}
}

func TestThresholdAndSide(t *testing.T) {
	if !MeetsThreshold(d("-0.8"), d("0.8")) || !MeetsThreshold(d("0.8"), d("0.8")) || MeetsThreshold(d("0.79"), d("0.8")) {
		t.Fatal("threshold uses |gap| >= safe")
	}
	if MasterSide(d("1")) != "sell" || MasterSide(d("-1")) != "buy" || MasterSide(decimal.Zero) != "" {
		t.Fatal("side sign logic")
	}
	if HedgeSide("sell") != "buy" || HedgeSide("buy") != "sell" {
		t.Fatal("hedge side")
	}
}

func TestPriceAndAmount(t *testing.T) {
	if p := LimitPrice("sell", d("999"), d("1000"), d("0.0001")); !p.Equal(d("999.90")) {
		t.Fatalf("sell price %s", p)
	}
	if p := LimitPrice("buy", d("1000"), d("1001"), d("0.0001")); !p.Equal(d("1000.10")) {
		t.Fatalf("buy price %s", p)
	}
	if a := OrderAmount(d("100"), d("999.90"), 4); !a.Equal(d("0.1000")) {
		t.Fatalf("amount %s", a)
	}
	if a := OrderAmount(d("100"), d("3"), 0); !a.Equal(d("33")) {
		t.Fatalf("xrp-style amount %s", a)
	}
	if !OrderAmount(d("100"), decimal.Zero, 2).IsZero() {
		t.Fatal("zero price")
	}
}

func TestProfit(t *testing.T) {
	// master bought at 100, slave sold at 101 MYR, 2 units -> +2
	if p := Profit("buy", d("100"), d("101"), d("2")); !p.Equal(d("2")) {
		t.Fatal(p)
	}
	// master sold at 100, slave bought at 99 -> +2
	if p := Profit("sell", d("100"), d("99"), d("2")); !p.Equal(d("2")) {
		t.Fatal(p)
	}
}

func TestBalancesSufficient(t *testing.T) {
	rate, size := d("4"), d("100")
	b := Balances{MasterAssetFree: d("0.01"), MasterQuoteFree: d("1000"), SlaveAssetFree: d("0.01"),
		SlaveQuoteFree: d("500"), MasterLast: d("20000"), SlaveLast: d("5000")}
	// sell: asset value 200 >= 100; slave 2000 MYR >= 100*2
	if ok, _ := BalancesSufficient("sell", b, rate, size, 2); !ok {
		t.Fatal("sell should pass")
	}
	b.SlaveQuoteFree = d("40") // 160 MYR < 200
	if ok, _ := BalancesSufficient("sell", b, rate, size, 2); ok {
		t.Fatal("slave quote too low")
	}
	b.SlaveQuoteFree = d("500")
	b.MasterAssetFree = d("0.001") // 20 MYR < 100
	if ok, _ := BalancesSufficient("sell", b, rate, size, 2); ok {
		t.Fatal("master asset too low")
	}
	// buy: master MYR 1000 >= 200; slave asset 0.01*5000*4=200 >= 100
	if ok, _ := BalancesSufficient("buy", b, rate, size, 2); !ok {
		t.Fatal("buy should pass")
	}
	b.MasterQuoteFree = d("199")
	if ok, _ := BalancesSufficient("buy", b, rate, size, 2); ok {
		t.Fatal("master quote too low")
	}
	b.MasterQuoteFree = d("1000")
	b.SlaveAssetFree = d("0.001")
	if ok, why := BalancesSufficient("buy", b, rate, size, 2); ok || why == "" {
		t.Fatal("slave asset too low")
	}
}

var sink decimal.Decimal

// BenchmarkGapHotPath measures the pure gap + slave-choice computation
// (no network, no I/O) for two slaves.
func BenchmarkGapHotPath(b *testing.B) {
	last, rate := d("350000.25"), d("4.1853")
	slaves := []SlaveQuote{{"A", d("83912.40")}, {"B", d("83890.15")}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, g, _ := ChooseSlave(last, rate, slaves)
		sink = g
	}
}

func BenchmarkGapPercent(b *testing.B) {
	last, bid, rate := d("350000.25"), d("83912.40"), d("4.1853")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink = GapPercent(last, bid, rate)
	}
}
