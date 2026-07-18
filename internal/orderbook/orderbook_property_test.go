package orderbook

import (
	"sort"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// **Validates: Requirements 2.1, 2.2**

func genPrice(t *rapid.T, label string) decimal.Decimal {
	cents := rapid.IntRange(1, 9999999).Draw(t, label)
	return decimal.NewFromInt(int64(cents)).Div(decimal.NewFromInt(100))
}

func genQty(t *rapid.T, label string) decimal.Decimal {
	units := rapid.IntRange(1, 100000).Draw(t, label)
	return decimal.NewFromInt(int64(units)).Div(decimal.NewFromInt(1000))
}

func genLevels(t *rapid.T, label string) []PriceLevel {
	count := rapid.IntRange(0, 20).Draw(t, label+"_n")
	levels := make([]PriceLevel, 0, count)
	used := make(map[string]bool)
	for i := 0; i < count; i++ {
		p := genPrice(t, label+"_p")
		if used[p.String()] {
			continue
		}
		used[p.String()] = true
		levels = append(levels, PriceLevel{
			Price: p, Quantity: genQty(t, label+"_q"),
		})
	}
	return levels
}

func bidsDesc(ls []PriceLevel) bool {
	for i := 1; i < len(ls); i++ {
		if ls[i].Price.GreaterThanOrEqual(ls[i-1].Price) {
			return false
		}
	}
	return true
}

func asksAsc(ls []PriceLevel) bool {
	for i := 1; i < len(ls); i++ {
		if ls[i].Price.LessThanOrEqual(ls[i-1].Price) {
			return false
		}
	}
	return true
}

func noZero(ls []PriceLevel) bool {
	for _, l := range ls {
		if l.Quantity.IsZero() {
			return false
		}
	}
	return true
}

func TestProperty_IncrementalUpdate_SortingInvariant(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		sym := "TEST-USDT"
		ob.UpdateFromSnapshot(sym, &OrderBookSnapshot{
			Symbol: sym, Bids: genLevels(t, "ib"), Asks: genLevels(t, "ia"),
			SequenceID: 100, Timestamp: 1000,
		})
		delta := &OrderBookDelta{
			Symbol: sym, Bids: genLevels(t, "db"), Asks: genLevels(t, "da"),
			SequenceID: 101, Timestamp: 1001,
		}
		if err := ob.UpdateIncremental(sym, delta); err != nil {
			t.Fatalf("delta failed: %v", err)
		}
		ob.mu.RLock()
		bk := ob.books[sym]
		bc := make([]PriceLevel, len(bk.bids))
		copy(bc, bk.bids)
		ac := make([]PriceLevel, len(bk.asks))
		copy(ac, bk.asks)
		ob.mu.RUnlock()
		if len(bc) > 1 && !bidsDesc(bc) {
			t.Fatalf("bids not descending: %v", bc)
		}
		if len(ac) > 1 && !asksAsc(ac) {
			t.Fatalf("asks not ascending: %v", ac)
		}
	})
}

func TestProperty_IncrementalUpdate_ZeroQuantityRemoval(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		sym := "TEST-USDT"
		ob.UpdateFromSnapshot(sym, &OrderBookSnapshot{
			Symbol: sym, Bids: genLevels(t, "ib"), Asks: genLevels(t, "ia"),
			SequenceID: 100, Timestamp: 1000,
		})
		ob.mu.RLock()
		bk := ob.books[sym]
		eBids := make([]PriceLevel, len(bk.bids))
		copy(eBids, bk.bids)
		eAsks := make([]PriceLevel, len(bk.asks))
		copy(eAsks, bk.asks)
		ob.mu.RUnlock()

		var rmB []PriceLevel
		if len(eBids) > 0 {
			n := rapid.IntRange(0, len(eBids)).Draw(t, "nrb")
			for i := 0; i < n; i++ {
				rmB = append(rmB, PriceLevel{Price: eBids[i].Price, Quantity: decimal.Zero})
			}
		}
		var rmA []PriceLevel
		if len(eAsks) > 0 {
			n := rapid.IntRange(0, len(eAsks)).Draw(t, "nra")
			for i := 0; i < n; i++ {
				rmA = append(rmA, PriceLevel{Price: eAsks[i].Price, Quantity: decimal.Zero})
			}
		}

		delta := &OrderBookDelta{
			Symbol: sym, Bids: rmB, Asks: rmA,
			SequenceID: 101, Timestamp: 1001,
		}
		if err := ob.UpdateIncremental(sym, delta); err != nil {
			t.Fatalf("delta failed: %v", err)
		}

		ob.mu.RLock()
		bk = ob.books[sym]
		fB := make([]PriceLevel, len(bk.bids))
		copy(fB, bk.bids)
		fA := make([]PriceLevel, len(bk.asks))
		copy(fA, bk.asks)
		ob.mu.RUnlock()

		if !noZero(fB) {
			t.Fatal("bids contain zero qty")
		}
		if !noZero(fA) {
			t.Fatal("asks contain zero qty")
		}
		for _, r := range rmB {
			for _, b := range fB {
				if b.Price.Equal(r.Price) {
					t.Fatalf("bid %s not removed", r.Price)
				}
			}
		}
		for _, r := range rmA {
			for _, a := range fA {
				if a.Price.Equal(r.Price) {
					t.Fatalf("ask %s not removed", r.Price)
				}
			}
		}
	})
}

func TestProperty_IncrementalUpdate_NewPriceInsertion(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		sym := "TEST-USDT"
		ob.UpdateFromSnapshot(sym, &OrderBookSnapshot{
			Symbol: sym, Bids: genLevels(t, "ib"), Asks: genLevels(t, "ia"),
			SequenceID: 100, Timestamp: 1000,
		})

		ob.mu.RLock()
		bk := ob.books[sym]
		bpMap := make(map[string]bool)
		for _, b := range bk.bids {
			bpMap[b.Price.String()] = true
		}
		apMap := make(map[string]bool)
		for _, a := range bk.asks {
			apMap[a.Price.String()] = true
		}
		ob.mu.RUnlock()

		nbp := genPrice(t, "nbp")
		for bpMap[nbp.String()] {
			nbp = nbp.Add(decimal.NewFromInt(1))
		}
		nbq := genQty(t, "nbq")

		nap := genPrice(t, "nap")
		for apMap[nap.String()] {
			nap = nap.Add(decimal.NewFromInt(1))
		}
		naq := genQty(t, "naq")

		delta := &OrderBookDelta{
			Symbol:     sym,
			Bids:       []PriceLevel{{Price: nbp, Quantity: nbq}},
			Asks:       []PriceLevel{{Price: nap, Quantity: naq}},
			SequenceID: 101, Timestamp: 1001,
		}
		if err := ob.UpdateIncremental(sym, delta); err != nil {
			t.Fatalf("delta failed: %v", err)
		}

		ob.mu.RLock()
		bk = ob.books[sym]
		bc := make([]PriceLevel, len(bk.bids))
		copy(bc, bk.bids)
		ac := make([]PriceLevel, len(bk.asks))
		copy(ac, bk.asks)
		ob.mu.RUnlock()

		fb := false
		for _, b := range bc {
			if b.Price.Equal(nbp) && b.Quantity.Equal(nbq) {
				fb = true
				break
			}
		}
		if !fb {
			t.Fatalf("new bid %s not found", nbp)
		}
		fa := false
		for _, a := range ac {
			if a.Price.Equal(nap) && a.Quantity.Equal(naq) {
				fa = true
				break
			}
		}
		if !fa {
			t.Fatalf("new ask %s not found", nap)
		}
		if len(bc) > 1 && !bidsDesc(bc) {
			t.Fatal("bids not descending after insert")
		}
		if len(ac) > 1 && !asksAsc(ac) {
			t.Fatal("asks not ascending after insert")
		}
	})
}

func TestProperty_IncrementalUpdate_ExistingPriceUpdate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		sym := "TEST-USDT"

		nB := rapid.IntRange(1, 15).Draw(t, "nB")
		nA := rapid.IntRange(1, 15).Draw(t, "nA")
		bpU := make(map[string]bool)
		var iB []PriceLevel
		for i := 0; i < nB; i++ {
			p := genPrice(t, "bp")
			if bpU[p.String()] {
				continue
			}
			bpU[p.String()] = true
			iB = append(iB, PriceLevel{Price: p, Quantity: genQty(t, "bq")})
		}
		apU := make(map[string]bool)
		var iA []PriceLevel
		for i := 0; i < nA; i++ {
			p := genPrice(t, "ap")
			if apU[p.String()] {
				continue
			}
			apU[p.String()] = true
			iA = append(iA, PriceLevel{Price: p, Quantity: genQty(t, "aq")})
		}

		ob.UpdateFromSnapshot(sym, &OrderBookSnapshot{
			Symbol: sym, Bids: iB, Asks: iA,
			SequenceID: 100, Timestamp: 1000,
		})

		ob.mu.RLock()
		bk := ob.books[sym]
		sB := make([]PriceLevel, len(bk.bids))
		copy(sB, bk.bids)
		sA := make([]PriceLevel, len(bk.asks))
		copy(sA, bk.asks)
		ob.mu.RUnlock()

		var uB []PriceLevel
		if len(sB) > 0 {
			idx := rapid.IntRange(0, len(sB)-1).Draw(t, "ubi")
			uB = append(uB, PriceLevel{Price: sB[idx].Price, Quantity: genQty(t, "ubq")})
		}
		var uA []PriceLevel
		if len(sA) > 0 {
			idx := rapid.IntRange(0, len(sA)-1).Draw(t, "uai")
			uA = append(uA, PriceLevel{Price: sA[idx].Price, Quantity: genQty(t, "uaq")})
		}

		delta := &OrderBookDelta{
			Symbol: sym, Bids: uB, Asks: uA,
			SequenceID: 101, Timestamp: 1001,
		}
		if err := ob.UpdateIncremental(sym, delta); err != nil {
			t.Fatalf("delta failed: %v", err)
		}

		ob.mu.RLock()
		bk = ob.books[sym]
		fB := make([]PriceLevel, len(bk.bids))
		copy(fB, bk.bids)
		fA := make([]PriceLevel, len(bk.asks))
		copy(fA, bk.asks)
		ob.mu.RUnlock()

		for _, u := range uB {
			found := false
			for _, b := range fB {
				if b.Price.Equal(u.Price) {
					if !b.Quantity.Equal(u.Quantity) {
						t.Fatalf("bid %s: want %s got %s", u.Price, u.Quantity, b.Quantity)
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("bid %s missing", u.Price)
			}
		}
		for _, u := range uA {
			found := false
			for _, a := range fA {
				if a.Price.Equal(u.Price) {
					if !a.Quantity.Equal(u.Quantity) {
						t.Fatalf("ask %s: want %s got %s", u.Price, u.Quantity, a.Quantity)
					}
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("ask %s missing", u.Price)
			}
		}
		if len(fB) > 1 && !bidsDesc(fB) {
			t.Fatal("bids not descending after qty update")
		}
		if len(fA) > 1 && !asksAsc(fA) {
			t.Fatal("asks not ascending after qty update")
		}
	})
}

func TestProperty_IncrementalUpdate_MultipleDeltas(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		sym := "TEST-USDT"
		ob.UpdateFromSnapshot(sym, &OrderBookSnapshot{
			Symbol: sym, Bids: genLevels(t, "ib"), Asks: genLevels(t, "ia"),
			SequenceID: 100, Timestamp: 1000,
		})

		nd := rapid.IntRange(1, 10).Draw(t, "nd")
		for i := 0; i < nd; i++ {
			var dB []PriceLevel
			nbu := rapid.IntRange(0, 5).Draw(t, "nbu")
			for j := 0; j < nbu; j++ {
				p := genPrice(t, "dbp")
				rm := rapid.IntRange(0, 4).Draw(t, "rm") == 0
				var q decimal.Decimal
				if rm {
					q = decimal.Zero
				} else {
					q = genQty(t, "dbq")
				}
				dB = append(dB, PriceLevel{Price: p, Quantity: q})
			}
			var dA []PriceLevel
			nau := rapid.IntRange(0, 5).Draw(t, "nau")
			for j := 0; j < nau; j++ {
				p := genPrice(t, "dap")
				rm := rapid.IntRange(0, 4).Draw(t, "rm") == 0
				var q decimal.Decimal
				if rm {
					q = decimal.Zero
				} else {
					q = genQty(t, "daq")
				}
				dA = append(dA, PriceLevel{Price: p, Quantity: q})
			}
			delta := &OrderBookDelta{
				Symbol: sym, Bids: dB, Asks: dA,
				SequenceID: int64(101 + i), Timestamp: int64(1001 + i),
			}
			if err := ob.UpdateIncremental(sym, delta); err != nil {
				t.Fatalf("delta %d failed: %v", i, err)
			}
			ob.mu.RLock()
			bk := ob.books[sym]
			cb := make([]PriceLevel, len(bk.bids))
			copy(cb, bk.bids)
			ca := make([]PriceLevel, len(bk.asks))
			copy(ca, bk.asks)
			ob.mu.RUnlock()
			if len(cb) > 1 && !bidsDesc(cb) {
				t.Fatalf("bids not descending after delta %d", i)
			}
			if len(ca) > 1 && !asksAsc(ca) {
				t.Fatalf("asks not ascending after delta %d", i)
			}
			if !noZero(cb) {
				t.Fatalf("bids zero qty after delta %d", i)
			}
			if !noZero(ca) {
				t.Fatalf("asks zero qty after delta %d", i)
			}
		}
	})
}

func TestProperty_Snapshot_SortingInvariant(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ob := NewLocalOrderBook()
		sym := "TEST-USDT"
		ob.UpdateFromSnapshot(sym, &OrderBookSnapshot{
			Symbol: sym, Bids: genLevels(t, "b"), Asks: genLevels(t, "a"),
			SequenceID: rapid.Int64Range(1, 10000).Draw(t, "seq"),
			Timestamp:  rapid.Int64Range(1, 999999).Draw(t, "ts"),
		})
		ob.mu.RLock()
		bk := ob.books[sym]
		bc := make([]PriceLevel, len(bk.bids))
		copy(bc, bk.bids)
		ac := make([]PriceLevel, len(bk.asks))
		copy(ac, bk.asks)
		ob.mu.RUnlock()
		if !sort.SliceIsSorted(bc, func(i, j int) bool {
			return bc[i].Price.GreaterThan(bc[j].Price)
		}) {
			t.Fatal("snapshot bids not descending")
		}
		if !sort.SliceIsSorted(ac, func(i, j int) bool {
			return ac[i].Price.LessThan(ac[j].Price)
		}) {
			t.Fatal("snapshot asks not ascending")
		}
	})
}
