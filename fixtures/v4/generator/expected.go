package workload

import (
	"fmt"
	"sort"
	"time"
)

// ResultTable is a task's expected result in canonical string form, emitted
// as CSV under fixtures/v4/<module>/expected/ and used by the evidence
// grading channel. Rows are sorted by their first column.
type ResultTable struct {
	Columns []string
	Rows    [][]string
}

func (r *ResultTable) sortRows() {
	sort.Slice(r.Rows, func(i, j int) bool { return r.Rows[i][0] < r.Rows[j][0] })
}

// Expectation is the machine-checkable outcome of one task: substrings the
// final answer must contain and the full expected result table.
type Expectation struct {
	Contains []string
	Result   *ResultTable
}

// oneRow builds a single-row expectation whose answer check requires the
// contains values.
func oneRow(contains []string, columns []string, row ...string) Expectation {
	return Expectation{Contains: contains,
		Result: &ResultTable{Columns: columns, Rows: [][]string{row}}}
}

// money renders minor units in display units for scale-2 currencies
// ("1234.56"); scale-0 currencies render plain. No thousands separators:
// the grader normalizes digit-group separators away.
func money(minor int64, scale int) string {
	if scale == 0 {
		return fmt.Sprintf("%d", minor)
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

func itoa(n int) string     { return fmt.Sprintf("%d", n) }
func i64toa(n int64) string { return fmt.Sprintf("%d", n) }

// monthRange returns the epoch-day range [from, to) of a 2025 month (1-12).
var monthStarts = []int{0, 31, 59, 90, 120, 151, 181}

func monthRange(month int) (int, int) { return monthStarts[month-1], monthStarts[month] }

// Expected computes every task expectation from the same deterministic
// plans that generated the tables. Task IDs match fixtures/v4 task files.
func Expected(cfg Config) map[string]Expectation {
	out := map[string]Expectation{}
	d := Generate(cfg)
	plans := paymentPlans(d)
	expectCommerce(d, out)
	expectPayments(d, plans, out)
	expectLedger(cfg, d, plans, out)
	expectLive(cfg, out)
	return out
}

func expectCommerce(d *Dataset, out map[string]Expectation) {
	out["commerce.active-customers-by-country"] = expectActiveCustomers(d)
	out["commerce.completed-revenue-app1-june"] = expectApp1JuneRevenue(d)
	out["commerce.orders-vs-items-grain"] = expectMarchGrain(d)

	// commerce.historical-price: product 2 unit price on Feb 1 vs Apr 1
	// (price version 2 applies from 2025-03-01).
	feb, apr := priceMinorAt(2, 31), priceMinorAt(2, 90)
	out["commerce.historical-price"] = Expectation{
		Contains: []string{money(feb, 2), money(apr, 2)},
		Result: &ResultTable{Columns: []string{"as_of", "unit_price_usd"},
			Rows: [][]string{{"2025-02-01", money(feb, 2)}, {"2025-04-01", money(apr, 2)}}},
	}
}

// expectActiveCustomers: active = deleted_at IS NULL; the answer must carry
// the total and the CN count.
func expectActiveCustomers(d *Dataset) Expectation {
	customers := d.Table("wl_customers")
	ctryIdx := customers.ColumnIndex("country_code")
	delIdx := customers.ColumnIndex("deleted_at")
	byCountry := map[string]int{}
	total := 0
	for _, row := range customers.Rows {
		if row[delIdx] == nil {
			byCountry[row[ctryIdx].(string)]++
			total++
		}
	}
	table := &ResultTable{Columns: []string{"country_code", "active_customers"}}
	for _, c := range customerCountries {
		table.Rows = append(table.Rows, []string{c, itoa(byCountry[c])})
	}
	table.sortRows()
	return Expectation{Contains: []string{itoa(total), itoa(byCountry["CN"])}, Result: table}
}

// expectApp1JuneRevenue sums completed app-1 orders created in June 2025,
// answered in display USD.
func expectApp1JuneRevenue(d *Dataset) Expectation {
	juneFrom, juneTo := monthRange(6)
	orders := d.Table("wl_orders")
	appIdx := orders.ColumnIndex("application_id")
	statusIdx := orders.ColumnIndex("status")
	totalIdx := orders.ColumnIndex("total_minor")
	idIdx := orders.ColumnIndex("id")
	var revenue int64
	for _, row := range orders.Rows {
		dayOffset := orderDay(row[idIdx].(int))
		if row[appIdx].(int) == 1 && row[statusIdx].(string) == "completed" &&
			dayOffset >= juneFrom && dayOffset < juneTo {
			revenue += row[totalIdx].(int64)
		}
	}
	return oneRow([]string{money(revenue, 2)},
		[]string{"application_id", "revenue_usd"}, "1", money(revenue, 2))
}

// expectMarchGrain: order count vs line-item count for March orders.
func expectMarchGrain(d *Dataset) Expectation {
	marFrom, marTo := monthRange(3)
	orders := d.Table("wl_orders")
	idIdx := orders.ColumnIndex("id")
	countOrders, countItems := 0, 0
	for _, row := range orders.Rows {
		n := row[idIdx].(int)
		dayOffset := orderDay(n)
		if dayOffset >= marFrom && dayOffset < marTo {
			countOrders++
			countItems += 1 + n%3
		}
	}
	return oneRow([]string{itoa(countOrders), itoa(countItems)},
		[]string{"orders", "order_items"}, itoa(countOrders), itoa(countItems))
}

func expectPayments(d *Dataset, plans []paymentPlan, out map[string]Expectation) {
	expectPaymentCounts(plans, out)
	expectCallbacks(d, out)
	out["payments.routing-rule-on-may-1"] = oneRow([]string{"2"}, []string{"rule_version_id"}, "2")
}

// paymentTallies are the deterministic aggregates over the payment plans
// shared by several payment-task expectations.
type paymentTallies struct {
	mayIntents, mayAttempts, aprIntents, aprFirst, aprFinal, recovered int
	byChannel                                                          map[int]int
	partialCount                                                       int
	partialTotal                                                       int64
}

func tallyPayments(plans []paymentPlan) paymentTallies {
	t := paymentTallies{byChannel: map[int]int{}}
	mayFrom, mayTo := monthRange(5)
	aprFrom, aprTo := monthRange(4)
	for _, p := range plans {
		if !p.HasIntent {
			continue
		}
		if p.PayDay >= mayFrom && p.PayDay < mayTo {
			t.mayIntents++
			t.mayAttempts += p.Attempts
		}
		if p.PayDay >= aprFrom && p.PayDay < aprTo {
			t.aprIntents++
			if p.Attempts == 1 && p.Succeeded {
				t.aprFirst++
			}
			if p.Succeeded {
				t.aprFinal++
			}
		}
		if p.Succeeded {
			t.byChannel[p.finalChannel()]++
			if p.Attempts > 1 {
				t.recovered++
			}
		}
		if p.Refund == 2 {
			t.partialCount++
			t.partialTotal += p.refundMinor()
		}
	}
	return t
}

func expectPaymentCounts(plans []paymentPlan, out map[string]Expectation) {
	t := tallyPayments(plans)
	out["payments.intent-vs-attempt-counts"] = oneRow(
		[]string{itoa(t.mayIntents), itoa(t.mayAttempts)},
		[]string{"payment_intents", "payment_attempts"}, itoa(t.mayIntents), itoa(t.mayAttempts))
	out["payments.first-vs-final-success-april"] = oneRow(
		[]string{itoa(t.aprFirst), itoa(t.aprFinal)},
		[]string{"intents", "first_attempt_succeeded", "finally_succeeded"},
		itoa(t.aprIntents), itoa(t.aprFirst), itoa(t.aprFinal))
	out["payments.channel-switch-recovered"] = oneRow(
		[]string{itoa(t.recovered)}, []string{"recovered_intents"}, itoa(t.recovered))
	out["payments.partial-refunds"] = oneRow(
		[]string{itoa(t.partialCount)},
		[]string{"partial_refunds", "total_minor"}, itoa(t.partialCount), i64toa(t.partialTotal))

	channelTable := &ResultTable{Columns: []string{"channel", "succeeded_intents"}}
	for c := 1; c <= len(channelNames); c++ {
		channelTable.Rows = append(channelTable.Rows, []string{channelNames[c-1], itoa(t.byChannel[c])})
	}
	channelTable.sortRows()
	out["payments.final-success-by-channel"] = Expectation{
		Contains: []string{"stripe", itoa(t.byChannel[1])}, Result: channelTable,
	}
}

// expectCallbacks: duplicate deliveries (rows beyond the first delivery of
// an external event) and unprocessed callbacks (processed_at IS NULL).
func expectCallbacks(d *Dataset, out map[string]Expectation) {
	callbacks := d.Table("wl_channel_callbacks")
	extIdx := callbacks.ColumnIndex("external_event_id")
	procIdx := callbacks.ColumnIndex("processed_at")
	seen := map[string]bool{}
	duplicates, unprocessed := 0, 0
	for _, row := range callbacks.Rows {
		id := row[extIdx].(string)
		if seen[id] {
			duplicates++
		}
		seen[id] = true
		if row[procIdx] == nil {
			unprocessed++
		}
	}
	out["payments.duplicate-callbacks"] = oneRow(
		[]string{itoa(duplicates)}, []string{"duplicate_deliveries"}, itoa(duplicates))
	out["payments.unprocessed-callbacks"] = oneRow(
		[]string{itoa(unprocessed)}, []string{"unprocessed_callbacks"}, itoa(unprocessed))
}

func expectLedger(cfg Config, d *Dataset, plans []paymentPlan, out map[string]Expectation) {
	unposted := 0
	var mayFees int64
	mayFrom, mayTo := monthRange(5)
	for _, p := range plans {
		if !p.HasIntent || !p.Succeeded {
			continue
		}
		if !p.Posted && cfg.Anomalies.UnpostedLedger {
			unposted++
		}
		if p.PayDay >= mayFrom && p.PayDay < mayTo {
			mayFees += p.feeMinor()
		}
	}
	out["ledger.succeeded-unposted"] = oneRow(
		[]string{itoa(unposted)}, []string{"succeeded_unposted_intents"}, itoa(unposted))
	out["ledger.fees-may"] = oneRow(
		[]string{i64toa(mayFees)}, []string{"month", "fee_minor"}, "2025-05", i64toa(mayFees))
	out["ledger.reconciliation-exceptions"] = expectReconExceptions(d)
	out["ledger.balance-snapshot-march"] = expectMarchSnapshot(d)
	out["ledger.settlement-net-org1"] = expectOrg1Settlement(d)
}

func expectReconExceptions(d *Dataset) Expectation {
	recon := d.Table("wl_reconciliation_results")
	statusIdx := recon.ColumnIndex("status")
	byStatus := map[string]int{}
	for _, row := range recon.Rows {
		byStatus[row[statusIdx].(string)]++
	}
	table := &ResultTable{Columns: []string{"status", "count"}}
	var contains []string
	for _, s := range []string{"amount_mismatch", "missing_external", "missing_internal"} {
		table.Rows = append(table.Rows, []string{s, itoa(byStatus[s])})
		contains = append(contains, itoa(byStatus[s]))
	}
	table.sortRows()
	return Expectation{Contains: contains, Result: table}
}

// expectMarchSnapshot: account 3 (org-1 payable) balance at the 2025-03-31
// snapshot (epoch day 89).
func expectMarchSnapshot(d *Dataset) Expectation {
	snapshots := d.Table("wl_balance_snapshots")
	acctIdx := snapshots.ColumnIndex("account_id")
	dateIdx := snapshots.ColumnIndex("snapshot_date")
	balIdx := snapshots.ColumnIndex("balance_minor")
	var balance int64
	for _, row := range snapshots.Rows {
		if row[acctIdx].(int) == 3 && fmtDate(row[dateIdx].(time.Time)) == "2025-03-31" {
			balance = row[balIdx].(int64)
		}
	}
	return oneRow([]string{i64toa(balance)},
		[]string{"account_id", "snapshot_date", "balance_minor"},
		"3", "2025-03-31", i64toa(balance))
}

func expectOrg1Settlement(d *Dataset) Expectation {
	settlements := d.Table("wl_settlements")
	orgIdx := settlements.ColumnIndex("organization_id")
	netIdx := settlements.ColumnIndex("net_minor")
	var net int64
	for _, row := range settlements.Rows {
		if row[orgIdx].(int) == 1 {
			net = row[netIdx].(int64)
		}
	}
	return oneRow([]string{money(net, 2)},
		[]string{"organization_id", "net_usd"}, "1", money(net, 2))
}

func expectLive(cfg Config, out map[string]Expectation) {
	plans := giftPlans(cfg)
	out["live.room1-vs-best-session"] = expectRoom1(plans)
	out["live.creator1-settled-vs-pending"] = expectCreator1(plans)

	// live.gift-price-versions: Rose (gift 1) star price on Feb 1 vs May 1.
	feb, may := giftStarsPrice(1, 31), giftStarsPrice(1, 120)
	out["live.gift-price-versions"] = Expectation{
		Contains: []string{i64toa(feb), i64toa(may)},
		Result: &ResultTable{Columns: []string{"as_of", "price_stars"},
			Rows: [][]string{{"2025-02-01", i64toa(feb)}, {"2025-05-01", i64toa(may)}}},
	}

	duplicates := 0
	for _, p := range plans {
		if p.Duplicate {
			duplicates++
		}
	}
	out["live.duplicate-gift-events"] = oneRow([]string{itoa(duplicates)},
		[]string{"duplicate_events", "stars_impact"}, itoa(duplicates), "0")
}

// expectRoom1: lifetime processed stars of room 1's sessions vs its best
// single session (room r hosts sessions where (s-1)%numRooms+1 == r).
func expectRoom1(plans []giftPlan) Expectation {
	bySession := map[int]int64{}
	var total int64
	for _, p := range plans {
		if p.Status != "processed" || (p.Session-1)%numRooms+1 != 1 {
			continue
		}
		bySession[p.Session] += p.Stars
		total += p.Stars
	}
	var best int64
	for _, v := range bySession {
		if v > best {
			best = v
		}
	}
	return oneRow([]string{i64toa(total), i64toa(best)},
		[]string{"room_total_stars", "best_session_stars"}, i64toa(total), i64toa(best))
}

// expectCreator1: settled stars (before cutoff) vs pending stars (on/after
// cutoff) for creator 1.
func expectCreator1(plans []giftPlan) Expectation {
	var settled, pending int64
	for _, p := range plans {
		if p.Status != "processed" || p.Creator != 1 {
			continue
		}
		share := p.Stars * creatorShareBps(p.Day) / 10000
		if p.Day < creatorSettleCutoff {
			settled += share
		} else {
			pending += share
		}
	}
	return oneRow([]string{i64toa(settled), i64toa(pending)},
		[]string{"settled_stars", "pending_stars"}, i64toa(settled), i64toa(pending))
}
