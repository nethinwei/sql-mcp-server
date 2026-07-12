package workload

import (
	"fmt"
	"time"
)

// Ledger account IDs are stable: 1 = platform revenue, 2 = channel fees,
// 3..5 = merchant payable pools per organization. Business facts and ledger
// facts are separate by design: order success != payment success != posted
// != settled.

// settlementCutoffDay: payments succeeded before this epoch day (2025-06-01,
// day 151) are settled to the organization; later ones remain pending.
const settlementCutoffDay = 151

// orgOfOrder maps an order to its owning organization via the application.
func orgOfOrder(orderID int) int { return appOrg[(orderID-1)%5] }

// payableAccount maps an organization to its merchant payable account.
var payableAccount = map[int]int{1: 3, 2: 4, 3: 5}

// ledgerState accumulates the module's tables and per-org settlement sums.
type ledgerState struct {
	transactions, entries, settlements, statements, recon *Table
	txID, entryID, stmtID, reconID                        int
	settledGross, settledFee, settledRefund               map[int]int64
	settledCcy                                            map[int]string
	an                                                    Anomalies
}

func buildLedger(d *Dataset) {
	s := &ledgerState{
		transactions: ledgerTransactionsTable(),
		entries:      ledgerEntriesTable(),
		settlements:  settlementsTable(),
		statements:   externalStatementsTable(),
		recon:        reconciliationTable(),
		settledGross: map[int]int64{}, settledFee: map[int]int64{},
		settledRefund: map[int]int64{}, settledCcy: map[int]string{},
		an: d.Config.Anomalies,
	}
	intentID := 0
	for _, p := range paymentPlans(d) {
		if !p.HasIntent {
			continue
		}
		intentID++
		if !p.Succeeded {
			continue
		}
		s.postPayment(p, intentID)
		s.reconcile(p, intentID)
		s.accumulateSettlement(p)
	}
	s.addOrphanStatements()
	s.addSettlements()
	accounts := ledgerAccountsTable()
	d.Tables = append(d.Tables, accounts, feeRuleVersionsTable(), s.transactions,
		s.entries, s.settlements, s.statements, s.recon,
		balanceSnapshots(accounts, s.transactions, s.entries))
}

// postPayment posts the payment (and refund) ledger transactions.
func (s *ledgerState) postPayment(p paymentPlan, intentID int) {
	org := orgOfOrder(p.OrderID)
	fee := p.feeMinor()
	posted := attemptTime(p, p.Attempts).Add(30 * time.Minute)

	if p.Posted || !s.an.UnpostedLedger {
		s.txID++
		s.transactions.Rows = append(s.transactions.Rows,
			[]any{s.txID, "payment", intentID, "posted", posted})
		s.addEntries(p.Currency, [][3]any{
			{payableAccount[org], "credit", p.GrossMinor - fee},
			{2, "credit", fee},
			{1, "debit", p.GrossMinor},
		})
	}
	if p.Refund != 0 {
		s.txID++
		s.transactions.Rows = append(s.transactions.Rows,
			[]any{s.txID, "refund", intentID, "posted", posted.Add(72 * time.Hour)})
		refund := p.refundMinor()
		s.addEntries(p.Currency, [][3]any{
			{payableAccount[org], "debit", refund},
			{1, "credit", refund},
		})
	}
}

func (s *ledgerState) addEntries(currency string, entries [][3]any) {
	for _, e := range entries {
		s.entryID++
		s.entries.Rows = append(s.entries.Rows,
			[]any{s.entryID, s.txID, e[0], e[1], e[2], currency})
	}
}

// reconcile emits the external statement line (or its absence) and the
// reconciliation result for one succeeded payment.
func (s *ledgerState) reconcile(p paymentPlan, intentID int) {
	missingExternal := s.an.ReconciliationMismatches && p.OrderID%19 == 0
	amountMismatch := s.an.ReconciliationMismatches && p.OrderID%23 == 0 && !missingExternal
	s.reconID++
	if missingExternal {
		s.recon.Rows = append(s.recon.Rows, []any{
			s.reconID, intentID, nil, "missing_external", p.GrossMinor, at(181, 2, 0, 0),
		})
		return
	}
	s.stmtID++
	stmtAmount := p.GrossMinor
	if amountMismatch {
		stmtAmount -= 100
	}
	s.statements.Rows = append(s.statements.Rows, []any{
		s.stmtID, p.finalChannel(),
		fmt.Sprintf("ch-%s-%06d", channelNames[p.finalChannel()-1], intentID),
		stmtAmount, p.Currency, day(p.PayDay + 1),
	})
	status := "matched"
	var diff int64
	if amountMismatch {
		status, diff = "amount_mismatch", 100
	}
	s.recon.Rows = append(s.recon.Rows, []any{
		s.reconID, intentID, s.stmtID, status, diff, at(181, 2, 0, 0),
	})
}

func (s *ledgerState) accumulateSettlement(p paymentPlan) {
	if p.PayDay >= settlementCutoffDay {
		return
	}
	org := orgOfOrder(p.OrderID)
	s.settledGross[org] += p.GrossMinor
	s.settledFee[org] += p.feeMinor()
	s.settledRefund[org] += p.refundMinor()
	s.settledCcy[org] = p.Currency
}

// addOrphanStatements emits statement lines with no internal record
// (missing_internal).
func (s *ledgerState) addOrphanStatements() {
	if !s.an.ReconciliationMismatches {
		return
	}
	for i := 1; i <= 3; i++ {
		s.stmtID++
		s.statements.Rows = append(s.statements.Rows, []any{
			s.stmtID, i, fmt.Sprintf("ch-%s-orphan-%02d", channelNames[i-1], i),
			int64(10000 + i*111), "USD", day(100 + i),
		})
		s.reconID++
		s.recon.Rows = append(s.recon.Rows, []any{
			s.reconID, nil, s.stmtID, "missing_internal", -int64(10000 + i*111), at(181, 2, 0, 0),
		})
	}
}

func (s *ledgerState) addSettlements() {
	for org := 1; org <= 3; org++ {
		if s.settledCcy[org] == "" {
			continue
		}
		net := s.settledGross[org] - s.settledFee[org] - s.settledRefund[org]
		s.settlements.Rows = append(s.settlements.Rows, []any{
			org, org, day(0), day(settlementCutoffDay),
			s.settledGross[org], s.settledFee[org], s.settledRefund[org], net,
			s.settledCcy[org], "paid", at(settlementCutoffDay+2, 10, 0, 0),
		})
	}
}

// balanceSnapshots computes month-end balances of the merchant payable
// accounts from the posted entries (point-in-time facts, not flows).
func balanceSnapshots(accounts, transactions, entries *Table) *Table {
	t := &Table{
		Module: "ledger-settlement", Name: "wl_balance_snapshots", Entity: "balance_snapshots",
		Description: "Month-end account balance snapshots in minor units (a snapshot is a " +
			"point-in-time fact, not a flow; recomputing from ledger_entries must agree)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Snapshot ID"},
			{Name: "account_id", Type: ColInt, Description: "Ledger account ID"},
			{Name: "snapshot_date", Type: ColDate,
				Description: "Snapshot date (balance as of end of that day, UTC)"},
			{Name: "balance_minor", Type: ColBigInt,
				Description: "Balance in minor units at the snapshot instant"},
			{Name: "currency_code", Type: ColCode, Description: "Account currency"},
		},
	}
	snapshotDays := []int{30, 58, 89, 119, 150, 180} // month ends Jan..Jun 2025
	txPosted := map[int]time.Time{}
	for _, row := range transactions.Rows {
		txPosted[row[0].(int)] = row[4].(time.Time)
	}
	snapID := 0
	for _, acct := range []int{3, 4, 5} {
		ccy := accounts.Rows[acct-1][3].(string)
		for _, cut := range snapshotDays {
			snapID++
			t.Rows = append(t.Rows, []any{
				snapID, acct, day(cut),
				balanceAt(entries, txPosted, acct, at(cut, 23, 59, 59)), ccy,
			})
		}
	}
	return t
}

func balanceAt(entries *Table, txPosted map[int]time.Time, account int, cutoff time.Time) int64 {
	var bal int64
	for _, e := range entries.Rows {
		if e[2].(int) != account || txPosted[e[1].(int)].After(cutoff) {
			continue
		}
		amt := e[4].(int64)
		if e[3].(string) == "debit" {
			amt = -amt
		}
		bal += amt
	}
	return bal
}

func ledgerAccountsTable() *Table {
	return &Table{
		Module: "ledger-settlement", Name: "wl_ledger_accounts", Entity: "ledger_accounts",
		Description: "Internal ledger accounts (platform revenue, channel fee expense, " +
			"merchant payable pools)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Ledger account ID"},
			{Name: "account_type", Type: ColCode,
				Description: "platform_revenue | channel_fee | merchant_payable"},
			{Name: "owner_ref", Type: ColCode, Description: "Owner reference (org-N or platform)"},
			{Name: "currency_code", Type: ColCode, Description: "Account currency"},
		},
		Rows: [][]any{
			{1, "platform_revenue", "platform", "USD"},
			{2, "channel_fee", "platform", "USD"},
			{3, "merchant_payable", "org-1", "USD"},
			{4, "merchant_payable", "org-2", "CNY"},
			{5, "merchant_payable", "org-3", "KRW"},
		},
	}
}

func feeRuleVersionsTable() *Table {
	t := &Table{
		Module: "ledger-settlement", Name: "wl_fee_rule_versions", Entity: "fee_rule_versions",
		Description: "Versioned channel fee rules in basis points; a version applies to " +
			"payments whose success time falls in [valid_from, valid_to). Recomputing " +
			"history must use the version valid at payment time",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Fee rule version ID"},
			{Name: "channel_id", Type: ColInt, Description: "Channel ID"},
			{Name: "fee_bps", Type: ColInt,
				Description: "Fee in basis points (1/100 of a percent) of gross amount"},
			{Name: "valid_from", Type: ColDate, Description: "First day this version applies"},
			{Name: "valid_to", Type: ColDate, Nullable: true,
				Description: "First day this version no longer applies; NULL = current"},
		},
	}
	id := 0
	for c := 1; c <= len(channelNames); c++ {
		id++
		t.Rows = append(t.Rows, []any{id, c, int(feeBps(c, 0)), day(0), day(feeRuleSwitchDay)})
		id++
		t.Rows = append(t.Rows, []any{id, c, int(feeBps(c, feeRuleSwitchDay)), day(feeRuleSwitchDay), nil})
	}
	return t
}

func ledgerTransactionsTable() *Table {
	return &Table{
		Module: "ledger-settlement", Name: "wl_ledger_transactions", Entity: "ledger_transactions",
		Description: "Ledger transactions (immutable posting units); entries of one " +
			"transaction share its ID and balance to zero. A succeeded payment without a " +
			"posted transaction has not reached the ledger yet",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Ledger transaction ID"},
			{Name: "source_type", Type: ColCode, Description: "payment | refund"},
			{Name: "source_id", Type: ColInt,
				Description: "ID in the source entity (payment intent or refund)"},
			{Name: "status", Type: ColCode, Description: "posted"},
			{Name: "posted_at", Type: ColDateTime, Description: "Posting time"},
		},
	}
}

func ledgerEntriesTable() *Table {
	return &Table{
		Module: "ledger-settlement", Name: "wl_ledger_entries", Entity: "ledger_entries",
		Description: "Ledger entries: one debit/credit per row in minor units; direction is " +
			"debit or credit; a transaction's entries sum to zero per currency",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Entry ID"},
			{Name: "transaction_id", Type: ColInt, Description: "Ledger transaction ID"},
			{Name: "account_id", Type: ColInt, Description: "Ledger account ID"},
			{Name: "direction", Type: ColCode, Description: "debit | credit"},
			{Name: "amount_minor", Type: ColBigInt,
				Description: "Entry amount in minor units (always positive; sign comes from direction)"},
			{Name: "currency_code", Type: ColCode, Description: "Entry currency"},
		},
	}
}

func settlementsTable() *Table {
	return &Table{
		Module: "ledger-settlement", Name: "wl_settlements", Entity: "settlements",
		Description: "Settlements paid out to organizations; net_minor = gross - fees - " +
			"refunds for the covered period. Settled is distinct from posted and from bank arrival",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Settlement ID"},
			{Name: "organization_id", Type: ColInt, Description: "Organization ID"},
			{Name: "period_start", Type: ColDate, Description: "Covered period start (inclusive)"},
			{Name: "period_end", Type: ColDate, Description: "Covered period end (exclusive)"},
			{Name: "gross_minor", Type: ColBigInt, Description: "Gross captured amount in minor units"},
			{Name: "fee_minor", Type: ColBigInt, Description: "Channel fees in minor units"},
			{Name: "refund_minor", Type: ColBigInt, Description: "Refunded amount in minor units"},
			{Name: "net_minor", Type: ColBigInt, Description: "Net paid out in minor units"},
			{Name: "currency_code", Type: ColCode, Description: "Settlement currency"},
			{Name: "status", Type: ColCode, Description: "paid"},
			{Name: "settled_at", Type: ColDateTime, Description: "Payout execution time"},
		},
	}
}

func externalStatementsTable() *Table {
	return &Table{
		Module: "ledger-settlement", Name: "wl_external_statement_items", Entity: "external_statement_items",
		Description: "Line items from channel-issued statements (external side of " +
			"reconciliation); amounts and references come from the channel and can disagree " +
			"with internal records",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Statement item ID"},
			{Name: "channel_id", Type: ColInt, Description: "Channel ID"},
			{Name: "external_ref", Type: ColCode, Description: "Channel-side transaction reference"},
			{Name: "amount_minor", Type: ColBigInt, Description: "Channel-reported amount in minor units"},
			{Name: "currency_code", Type: ColCode, Description: "Channel-reported currency"},
			{Name: "statement_date", Type: ColDate,
				Description: "Statement date (channel timezone, date only)"},
		},
	}
}

func reconciliationTable() *Table {
	return &Table{
		Module: "ledger-settlement", Name: "wl_reconciliation_results", Entity: "reconciliation_results",
		Description: "Reconciliation outcome per succeeded payment: matched, " +
			"amount_mismatch, missing_external (internal success without a statement line), " +
			"or missing_internal (statement line without internal record)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Reconciliation row ID"},
			{Name: "intent_id", Type: ColInt, Nullable: true,
				Description: "Internal payment intent ID; NULL for missing_internal"},
			{Name: "statement_item_id", Type: ColInt, Nullable: true,
				Description: "External statement item ID; NULL for missing_external"},
			{Name: "status", Type: ColCode,
				Description: "matched | amount_mismatch | missing_external | missing_internal"},
			{Name: "difference_minor", Type: ColBigInt,
				Description: "Internal minus external amount in minor units (0 when matched)"},
			{Name: "checked_at", Type: ColDateTime, Description: "Reconciliation run time"},
		},
	}
}
