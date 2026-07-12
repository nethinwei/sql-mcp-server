package workload

import (
	"fmt"
	"time"
)

// Payment channels. Channel fee basis points come from fee rule versions in
// the ledger module.
var channelNames = []string{"stripe", "adyen", "alipay", "wechatpay", "kcp"}

// feeRuleSwitchDay is the epoch day (2025-04-01) when fee rule and routing
// rule v2 replace v1. Historical queries must use the version valid at
// transaction time.
const feeRuleSwitchDay = 90

// feeBps returns the channel fee in basis points valid on the given epoch
// day. Version 1 applies before feeRuleSwitchDay, version 2 after.
func feeBps(channel int, dayOffset int) int64 {
	if dayOffset < feeRuleSwitchDay {
		return int64(300 + channel*10)
	}
	return int64(280 + channel*10)
}

// paymentPlan is the deterministic payment outcome of one order. Derived
// from the order id only, so the payments, ledger, and expected-result
// builders all agree by construction.
type paymentPlan struct {
	OrderID    int
	HasIntent  bool
	Attempts   int  // number of attempts (1..3); attempts before the last one failed
	Succeeded  bool // final attempt (and the intent) succeeded
	Processing bool // final attempt still processing (intent not final)
	// FirstChannel is the routed channel of attempt 1; each retry moves one
	// channel forward (routing failover).
	FirstChannel int
	PayDay       int // epoch day of the final attempt
	GrossMinor   int64
	Currency     string
	// Refund: 0 = none, 1 = full, 2 = partial (40%, rounded down)
	Refund int
	// Posted reports whether the succeeded payment reached the ledger
	// (false = succeeded but not yet posted; UnpostedLedger anomaly).
	Posted bool
}

func (p paymentPlan) channelOfAttempt(attempt int) int {
	return (p.FirstChannel-1+attempt-1)%len(channelNames) + 1
}

func (p paymentPlan) finalChannel() int { return p.channelOfAttempt(p.Attempts) }

// refundMinor returns the refunded amount in minor units (0 when no refund).
func (p paymentPlan) refundMinor() int64 {
	switch p.Refund {
	case 1:
		return p.GrossMinor
	case 2:
		return p.GrossMinor * 40 / 100
	}
	return 0
}

// feeMinor is the channel fee of the final (successful) attempt.
func (p paymentPlan) feeMinor() int64 {
	return p.GrossMinor * feeBps(p.finalChannel(), p.PayDay) / 10000
}

// paymentPlans derives one plan per order from the generated order rows.
func paymentPlans(d *Dataset) []paymentPlan {
	orders := d.Table("wl_orders")
	idIdx := orders.ColumnIndex("id")
	statusIdx := orders.ColumnIndex("status")
	ccyIdx := orders.ColumnIndex("currency_code")
	totalIdx := orders.ColumnIndex("total_minor")

	plans := make([]paymentPlan, 0, len(orders.Rows))
	for _, row := range orders.Rows {
		plans = append(plans, orderPlan(d.Config.Anomalies,
			row[idIdx].(int), row[statusIdx].(string),
			row[ccyIdx].(string), row[totalIdx].(int64)))
	}
	return plans
}

func orderPlan(an Anomalies, n int, status, currency string, total int64) paymentPlan {
	p := paymentPlan{
		OrderID:      n,
		GrossMinor:   total,
		Currency:     currency,
		FirstChannel: (n-1)%len(channelNames) + 1,
		PayDay:       orderDay(n),
		Attempts:     1,
		Posted:       true,
	}
	if status == "cancelled" {
		return p
	}
	p.HasIntent = true
	if an.FailedAttempts {
		if n%3 == 0 {
			p.Attempts++
		}
		if n%7 == 0 {
			p.Attempts++
		}
	}
	if status == "confirmed" && n%8 == 1 {
		p.Processing = true
	} else {
		p.Succeeded = true
	}
	if p.Succeeded {
		switch n % 10 {
		case 5:
			p.Refund = 1
		case 8:
			p.Refund = 2
			if !an.PartialRefunds {
				p.Refund = 1
			}
		}
		if an.UnpostedLedger && n%17 == 0 {
			p.Posted = false
		}
	}
	return p
}

// attemptTime returns the creation time of one attempt: retries follow the
// first attempt at 10-minute intervals.
func attemptTime(p paymentPlan, attempt int) time.Time {
	return at(p.PayDay, 9, 0, 0).Add(time.Duration(attempt-1) * 10 * time.Minute)
}

// paymentTables groups the module's tables while rows accumulate.
type paymentTables struct {
	intents, attempts, decisions, refunds, callbacks    *Table
	intentID, attemptID, decisionID, refundID, callback int
}

func buildPayments(d *Dataset) {
	t := newPaymentTables()
	an := d.Config.Anomalies
	for _, p := range paymentPlans(d) {
		if !p.HasIntent {
			continue
		}
		t.addIntent(p)
		for a := 1; a <= p.Attempts; a++ {
			t.addAttempt(p, a, an)
		}
		t.addRefund(p)
	}
	d.Tables = append(d.Tables, paymentChannels(), routingRuleVersions(),
		t.intents, t.attempts, t.decisions, t.refunds, t.callbacks)
}

func (t *paymentTables) addIntent(p paymentPlan) {
	t.intentID++
	created := attemptTime(p, 1)
	var completed, finalChannel any
	var fee int64
	status := "processing"
	if p.Succeeded {
		status = "succeeded"
		completed = attemptTime(p, p.Attempts).Add(time.Minute)
		finalChannel = p.finalChannel()
		fee = p.feeMinor()
	}
	t.intents.Rows = append(t.intents.Rows, []any{
		t.intentID, p.OrderID, status, p.GrossMinor, fee, p.Currency,
		finalChannel, created, completed,
	})
}

func (t *paymentTables) addAttempt(p paymentPlan, a int, an Anomalies) {
	t.attemptID++
	st := "failed"
	channelSt := "DECLINED"
	if a == p.Attempts {
		if p.Succeeded {
			st, channelSt = "succeeded", "SETTLED"
		} else {
			st, channelSt = "processing", "PENDING"
		}
	}
	t.attempts.Rows = append(t.attempts.Rows, []any{
		t.attemptID, t.intentID, a, p.channelOfAttempt(a), st, channelSt,
		p.GrossMinor, attemptTime(p, a),
	})

	t.decisionID++
	ruleVersion := 1
	if p.PayDay >= feeRuleSwitchDay {
		ruleVersion = 2
	}
	reason := "primary"
	if a > 1 {
		reason = "retry_failover"
	}
	t.decisions.Rows = append(t.decisions.Rows, []any{
		t.decisionID, t.attemptID, ruleVersion, p.channelOfAttempt(a), reason,
	})

	t.addCallbacks(p, a, st, an)
}

// addCallbacks emits one callback per attempt outcome; some duplicated,
// some received late relative to occurrence, some unprocessed.
func (t *paymentTables) addCallbacks(p paymentPlan, a int, st string, an Anomalies) {
	occurred := attemptTime(p, a).Add(30 * time.Second)
	deliveries := 1
	if an.DuplicateCallbacks && t.attemptID%9 == 0 {
		deliveries = 2
	}
	for dlv := 0; dlv < deliveries; dlv++ {
		t.callback++
		received := occurred.Add(time.Duration(1+dlv*3) * time.Minute)
		if an.OutOfOrderCallbacks && t.attemptID%11 == 0 {
			received = occurred.Add(6 * time.Hour) // delayed past later events
		}
		var processed any = received.Add(5 * time.Second)
		if t.callback%23 == 0 {
			processed = nil // received but not processed
		}
		t.callbacks.Rows = append(t.callbacks.Rows, []any{
			t.callback, p.channelOfAttempt(a), t.attemptID,
			fmt.Sprintf("evt-%s-%06d", channelNames[p.channelOfAttempt(a)-1], t.attemptID),
			"payment_" + st, occurred, received, processed,
		})
	}
}

func (t *paymentTables) addRefund(p paymentPlan) {
	if p.Refund == 0 {
		return
	}
	t.refundID++
	kind := "full"
	if p.Refund == 2 {
		kind = "partial"
	}
	t.refunds.Rows = append(t.refunds.Rows, []any{
		t.refundID, t.intentID, kind, p.refundMinor(), p.Currency, "succeeded",
		attemptTime(p, p.Attempts).Add(72 * time.Hour),
	})
}

func paymentChannels() *Table {
	t := &Table{
		Module: "payment-orchestration", Name: "wl_payment_channels", Entity: "payment_channels",
		Description: "External payment channels (PSPs)",
		PrimaryKey:  []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Channel ID"},
			{Name: "name", Type: ColCode, Description: "Channel name"},
			{Name: "status", Type: ColCode, Description: "active | disabled"},
		},
	}
	for i, name := range channelNames {
		t.Rows = append(t.Rows, []any{i + 1, name, "active"})
	}
	return t
}

func routingRuleVersions() *Table {
	return &Table{
		Module: "payment-orchestration", Name: "wl_routing_rule_versions", Entity: "routing_rule_versions",
		Description: "Versioned routing rules; a version applies to attempts whose created " +
			"time falls in [valid_from, valid_to). Historical queries must use the version " +
			"valid at attempt time",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Routing rule version ID"},
			{Name: "rule_name", Type: ColCode, Description: "Rule name"},
			{Name: "strategy", Type: ColText, Description: "Routing strategy summary"},
			{Name: "valid_from", Type: ColDate, Description: "First day this version applies"},
			{Name: "valid_to", Type: ColDate, Nullable: true,
				Description: "First day this version no longer applies; NULL = current"},
		},
		Rows: [][]any{
			{1, "default-routing", "round-robin by order id; failover to next channel",
				day(0), day(feeRuleSwitchDay)},
			{2, "default-routing", "round-robin by order id; failover to next channel; kcp deprioritized",
				day(feeRuleSwitchDay), nil},
		},
	}
}

func newPaymentTables() *paymentTables {
	return &paymentTables{
		intents:   paymentIntentsTable(),
		attempts:  paymentAttemptsTable(),
		decisions: routingDecisionsTable(),
		refunds:   refundsTable(),
		callbacks: channelCallbacksTable(),
	}
}

func paymentIntentsTable() *Table {
	return &Table{
		Module: "payment-orchestration", Name: "wl_payment_intents", Entity: "payment_intents",
		Description: "Payment intents: one business payment goal per order (NOT a single " +
			"channel request — see payment_attempts). Intent success rate and attempt " +
			"success rate are different metrics",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Payment intent ID"},
			{Name: "order_id", Type: ColInt, Description: "Order ID"},
			{Name: "status", Type: ColCode, Description: "processing | succeeded"},
			{Name: "amount_minor", Type: ColBigInt, Description: "Intent amount in minor units"},
			{Name: "fee_minor", Type: ColBigInt,
				Description: "Channel fee in minor units of the succeeded attempt (0 while " +
					"processing), per the fee rule version valid at payment time"},
			{Name: "currency_code", Type: ColCode, Description: "Intent currency"},
			{Name: "final_channel_id", Type: ColInt, Nullable: true,
				Description: "Channel of the final (succeeded) attempt; NULL while processing"},
			{Name: "created_at", Type: ColDateTime, Description: "Intent creation time"},
			{Name: "completed_at", Type: ColDateTime, Nullable: true,
				Description: "Final success time; NULL while processing"},
		},
	}
}

func paymentAttemptsTable() *Table {
	return &Table{
		Module: "payment-orchestration", Name: "wl_payment_attempts", Entity: "payment_attempts",
		Description: "Individual payment attempts: one intent can have several (failed " +
			"attempts precede a retry on another channel). First-attempt result and " +
			"final intent result differ",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Attempt ID"},
			{Name: "intent_id", Type: ColInt, Description: "Payment intent ID"},
			{Name: "attempt_no", Type: ColInt, Description: "1-based sequence within the intent"},
			{Name: "channel_id", Type: ColInt, Description: "Routed channel ID"},
			{Name: "status", Type: ColCode, Description: "failed | processing | succeeded"},
			{Name: "channel_status", Type: ColCode,
				Description: "Raw channel status string (channel-specific vocabulary)"},
			{Name: "amount_minor", Type: ColBigInt, Description: "Attempt amount in minor units"},
			{Name: "created_at", Type: ColDateTime, Description: "Attempt creation time"},
		},
	}
}

func routingDecisionsTable() *Table {
	return &Table{
		Module: "payment-orchestration", Name: "wl_routing_decisions", Entity: "routing_decisions",
		Description: "Routing decision per attempt: candidates, selected channel, the " +
			"rule version in force at attempt time, and the reason",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Decision ID"},
			{Name: "attempt_id", Type: ColInt, Description: "Attempt ID"},
			{Name: "rule_version_id", Type: ColInt,
				Description: "Routing rule version in force at attempt time"},
			{Name: "selected_channel_id", Type: ColInt, Description: "Selected channel"},
			{Name: "reason", Type: ColCode, Description: "primary | retry_failover"},
		},
	}
}

func refundsTable() *Table {
	return &Table{
		Module: "payment-orchestration", Name: "wl_refunds", Entity: "refunds",
		Description: "Refunds against succeeded payment intents; partial refunds are " +
			"common (amount below the intent amount)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Refund ID"},
			{Name: "intent_id", Type: ColInt, Description: "Payment intent ID"},
			{Name: "kind", Type: ColCode, Description: "full | partial"},
			{Name: "amount_minor", Type: ColBigInt, Description: "Refunded amount in minor units"},
			{Name: "currency_code", Type: ColCode, Description: "Refund currency"},
			{Name: "status", Type: ColCode, Description: "succeeded"},
			{Name: "created_at", Type: ColDateTime, Description: "Refund creation time"},
		},
	}
}

func channelCallbacksTable() *Table {
	return &Table{
		Module: "payment-orchestration", Name: "wl_channel_callbacks", Entity: "channel_callbacks",
		Description: "Raw channel callback events: duplicates, delays, and out-of-order " +
			"delivery all occur. external_event_id identifies the channel-side event; " +
			"rows sharing it are duplicate deliveries. occurred_at (channel), received_at " +
			"(gateway), processed_at (internal, NULL = received but not processed) are " +
			"distinct times",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Callback row ID"},
			{Name: "channel_id", Type: ColInt, Description: "Channel ID"},
			{Name: "attempt_id", Type: ColInt, Description: "Payment attempt ID"},
			{Name: "external_event_id", Type: ColCode,
				Description: "Channel-side event ID (duplicate deliveries share it)"},
			{Name: "event_type", Type: ColCode,
				Description: "payment_failed | payment_processing | payment_succeeded"},
			{Name: "occurred_at", Type: ColDateTime, Description: "Event time on the channel side"},
			{Name: "received_at", Type: ColDateTime,
				Description: "Time the gateway received this delivery"},
			{Name: "processed_at", Type: ColDateTime, Nullable: true,
				Description: "Internal processing time; NULL = received but not yet processed"},
		},
	}
}
