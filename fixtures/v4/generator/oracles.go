package workload

import "strconv"

// Confounder is a wrong answer value tied to a specific failure mode.
type Confounder struct {
	Value           string
	FailureCategory string
}

// Oracle holds counterfactual wrong answers for diagnostic grading.
type Oracle struct {
	Confounders map[string]Confounder
}

// Oracles computes counterfactual values from the same deterministic data
// as Expected. Task IDs match fixtures/v4/tasks-v5 guided-metadata and
// additions natural/oracle tasks.
func Oracles(cfg Config) map[string]Oracle {
	d := Generate(cfg)
	plans := paymentPlans(d)
	out := paymentOracles(plans, tallyPayments(plans))
	for id, o := range ledgerOracles(d) {
		out[id] = o
	}
	for id, o := range commerceOracles(d) {
		out[id] = o
	}
	for id, o := range liveOracles(cfg) {
		out[id] = o
	}
	mirrorNatural(out,
		"payments.intent-vs-attempt-counts",
		"payments.first-vs-final-success-april",
		"ledger.balance-snapshot-march",
		"commerce.completed-revenue-app1-june",
		"ledger.succeeded-unposted",
		"live.room1-vs-best-session",
	)
	return out
}

func mirrorNatural(out map[string]Oracle, ids ...string) {
	for _, id := range ids {
		if o, ok := out[id]; ok {
			out[id+".natural"] = o
		}
	}
}

func paymentOracles(plans []paymentPlan, t paymentTallies) map[string]Oracle {
	out := map[string]Oracle{}
	attemptFirst, attemptFinal := 0, 0
	aprFrom, aprTo := monthRange(4)
	for _, p := range plans {
		if !p.HasIntent || p.PayDay < aprFrom || p.PayDay >= aprTo {
			continue
		}
		if p.Succeeded {
			attemptFinal++
		}
		if p.Attempts >= 1 {
			attemptFirst++
		}
	}
	out["payments.first-vs-final-success-april"] = Oracle{Confounders: map[string]Confounder{
		"attempt_grain": {Value: itoa(attemptFinal), FailureCategory: "grain"},
		"wrong_first":   {Value: itoa(attemptFirst), FailureCategory: "status_semantics"},
	}}
	out["payments.intent-vs-attempt-counts"] = Oracle{Confounders: map[string]Confounder{
		"swap_grain": {Value: itoa(t.mayAttempts), FailureCategory: "grain"},
	}}
	return out
}

func ledgerOracles(d *Dataset) map[string]Oracle {
	snap := expectMarchSnapshot(d)
	var wrongBalance int64
	if len(snap.Contains) > 0 {
		wrongBalance, _ = strconv.ParseInt(snap.Contains[0], 10, 64)
		wrongBalance = wrongBalance + wrongBalance/10 + 1
	}
	return map[string]Oracle{
		"ledger.balance-snapshot-march": {Confounders: map[string]Confounder{
			"flow_replay": {Value: i64toa(wrongBalance), FailureCategory: "time_semantics"},
		}},
		"ledger.fees-may": {Confounders: map[string]Confounder{
			"created_not_completed": {Value: "0", FailureCategory: "time_semantics"},
		}},
		"ledger.succeeded-unposted": {Confounders: map[string]Confounder{
			"all_succeeded": {Value: "0", FailureCategory: "status_semantics"},
		}},
	}
}

func commerceOracles(d *Dataset) map[string]Oracle {
	return map[string]Oracle{
		"commerce.completed-revenue-app1-june": {Confounders: map[string]Confounder{
			"minor_not_converted": {Value: i64toa(juneRevenueMinor(d)), FailureCategory: "unit_currency"},
		}},
		"commerce.orders-vs-items-grain": {Confounders: map[string]Confounder{
			"item_as_order": {Value: expectMarchGrain(d).Contains[1], FailureCategory: "grain"},
		}},
	}
}

func liveOracles(cfg Config) map[string]Oracle {
	return map[string]Oracle{
		"live.room1-vs-best-session": {Confounders: map[string]Confounder{
			"session_as_room": {
				Value: i64toa(room1BestSession(giftPlans(cfg))), FailureCategory: "grain",
			},
		}},
	}
}

func juneRevenueMinor(d *Dataset) int64 {
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
	return revenue
}

func room1BestSession(plans []giftPlan) int64 {
	bySession := map[int]int64{}
	for _, p := range plans {
		if p.Status != "processed" || (p.Session-1)%numRooms+1 != 1 {
			continue
		}
		bySession[p.Session] += p.Stars
	}
	var best int64
	for _, v := range bySession {
		if v > best {
			best = v
		}
	}
	return best
}
