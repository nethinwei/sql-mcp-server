package workload

import (
	"fmt"
	"time"
)

// Live monetization: creators stream sessions in rooms; viewers spend the
// "star" virtual asset on gifts; each processed gift event splits into
// creator / agency / platform revenue by the rule version valid at event
// time. Virtual-asset amounts are in stars (integer), not fiat.

const (
	numCreators     = 8
	numAgencies     = 3
	numRooms        = 8
	numSessions     = 40
	numGiftEvents   = 150
	giftEventPeriod = 180 // events spread over days 0..179
)

// splitSwitchDay is the epoch day (2025-04-01) when revenue share v2
// replaces v1 (creator share rises from 50% to 55%).
const splitSwitchDay = 90

// creatorShareBps returns the creator share (basis points) valid on the day.
func creatorShareBps(dayOffset int) int64 {
	if dayOffset < splitSwitchDay {
		return 5000
	}
	return 5500
}

// agencyShareBps is the agency share for signed creators (creator id not
// divisible by 3 is signed to an agency; independent creators give 0).
func agencyShareBps(creator int) int64 {
	if creator%3 == 0 {
		return 0 // independent creator
	}
	return 1000
}

// giftStarsPrice returns the star price of gift g on the given day. Gift 1
// gets a price bump from day 90 (versioned pricing); others keep one price.
func giftStarsPrice(g int, dayOffset int) int64 {
	base := int64(g) * 10
	if g == 1 && dayOffset >= splitSwitchDay {
		return base + 5
	}
	return base
}

// giftPlan is the deterministic outcome of one gift event, shared by the
// table builders and the expected-result computations.
type giftPlan struct {
	ID        int
	Creator   int
	Session   int
	Day       int
	Gift      int
	Quantity  int64
	Stars     int64 // total stars charged (0 when charge failed)
	Status    string
	Duplicate bool // duplicate delivery of the previous event
}

// giftPlans derives every gift event deterministically from the seed. Above
// the base count the day cycle repeats, so any scale covers the same period.
func giftPlans(cfg Config) []giftPlan {
	total := numGiftEvents * cfg.Scale
	plans := make([]giftPlan, 0, total)
	for n := 1; n <= total; n++ {
		plan := oneGiftPlan(n)
		plans = append(plans, plan)
		if cfg.Anomalies.DuplicateGiftEvents && n%31 == 0 {
			dup := plan
			dup.Stars, dup.Status, dup.Duplicate = 0, "duplicate", true
			plans = append(plans, dup)
		}
	}
	return plans
}

func oneGiftPlan(n int) giftPlan {
	session := (n-1)%numSessions + 1
	dayOffset := ((n - 1) % numGiftEvents) * giftEventPeriod / numGiftEvents
	gift := (n-1)%5 + 1
	qty := int64(1 + n%3)
	status := "processed"
	stars := qty * giftStarsPrice(gift, dayOffset)
	switch n % 20 {
	case 6:
		status, stars = "charge_failed", 0
	case 13:
		status = "reversed" // charged then reversed (risk control)
	}
	return giftPlan{
		ID: n, Creator: (session-1)%numCreators + 1, Session: session,
		Day: dayOffset, Gift: gift, Quantity: qty, Stars: stars, Status: status,
	}
}

// creatorSettleCutoff mirrors the ledger settlement cutoff: creator revenue
// from events before day 151 (2025-06-01) is settled; later stays pending.
const creatorSettleCutoff = settlementCutoffDay

func buildLive(d *Dataset) {
	events, splits := liveEventsAndSplits(d.Config)
	d.Tables = append(d.Tables, agenciesTable(), creatorsTable(), liveRoomsTable(),
		liveSessionsTable(), giftDefinitionsTable(), giftPriceVersionsTable(),
		revenueShareRulesTable(), events, splits, creatorSettlementsTable(d.Config))
}

func liveEventsAndSplits(cfg Config) (*Table, *Table) {
	events := giftEventsTable()
	splits := revenueSplitsTable()
	rowID, splitID := 0, 0
	lastOriginalRow := 0
	for _, p := range giftPlans(cfg) {
		rowID++
		var dedupOf any
		if p.Duplicate {
			dedupOf = lastOriginalRow
		} else {
			lastOriginalRow = rowID
		}
		occurred := at(p.Day, 20, (p.ID*3)%60, 0)
		events.Rows = append(events.Rows, []any{
			rowID, p.Session, p.Creator, p.Gift, int(p.Quantity), p.Stars,
			p.Status, dedupOf, occurred,
		})
		if p.Status != "processed" {
			continue
		}
		ruleVersion := 1
		if p.Day >= splitSwitchDay {
			ruleVersion = 2
		}
		creatorStars := p.Stars * creatorShareBps(p.Day) / 10000
		agencyStars := p.Stars * agencyShareBps(p.Creator) / 10000
		splitID++
		splits.Rows = append(splits.Rows, []any{
			splitID, rowID, p.Creator, ruleVersion, creatorStars, agencyStars,
			p.Stars - creatorStars - agencyStars, occurred,
		})
	}
	return events, splits
}

func agenciesTable() *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_agencies", Entity: "agencies",
		Description: "Creator agencies (guilds) taking a revenue share from signed creators",
		PrimaryKey:  []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Agency ID"},
			{Name: "name", Type: ColText, Description: "Agency name"},
		},
	}
	for a := 1; a <= numAgencies; a++ {
		t.Rows = append(t.Rows, []any{a, fmt.Sprintf("Agency %d", a)})
	}
	return t
}

func creatorsTable() *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_creators", Entity: "creators",
		Description: "Live streamers; agency_id NULL means independent (no agency share)",
		PrimaryKey:  []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Creator ID"},
			{Name: "display_name", Type: ColText, Description: "Public name"},
			{Name: "agency_id", Type: ColInt, Nullable: true,
				Description: "Signing agency ID; NULL = independent"},
			{Name: "signed_at", Type: ColDate, Nullable: true,
				Description: "Agency signing date; NULL = independent"},
		},
	}
	for c := 1; c <= numCreators; c++ {
		var agency, signed any
		if c%3 != 0 {
			agency = (c-1)%numAgencies + 1
			signed = day(c)
		}
		t.Rows = append(t.Rows, []any{c, fmt.Sprintf("Creator %d", c), agency, signed})
	}
	return t
}

func liveRoomsTable() *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_live_rooms", Entity: "live_rooms",
		Description: "Long-lived live rooms (a room hosts many sessions; room lifetime " +
			"income != single-session income)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Room ID"},
			{Name: "creator_id", Type: ColInt, Description: "Owning creator ID"},
			{Name: "title", Type: ColText, Description: "Room title"},
		},
	}
	for r := 1; r <= numRooms; r++ {
		t.Rows = append(t.Rows, []any{r, (r-1)%numCreators + 1, fmt.Sprintf("Room %d", r)})
	}
	return t
}

func liveSessionsTable() *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_live_sessions", Entity: "live_sessions",
		Description: "Individual broadcast sessions of a room (session grain is finer than " +
			"room grain)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Session ID"},
			{Name: "room_id", Type: ColInt, Description: "Room ID"},
			{Name: "started_at", Type: ColDateTime, Description: "Broadcast start"},
			{Name: "ended_at", Type: ColDateTime, Description: "Broadcast end"},
		},
	}
	for s := 1; s <= numSessions; s++ {
		start := at((s-1)*giftEventPeriod/numSessions, 19, 0, 0)
		t.Rows = append(t.Rows, []any{s, (s-1)%numRooms + 1, start, start.Add(3 * time.Hour)})
	}
	return t
}

func giftDefinitionsTable() *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_gift_definitions", Entity: "gift_definitions",
		Description: "Gift catalog; star prices are versioned in gift_price_versions " +
			"(historical events keep the price at event time)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Gift ID"},
			{Name: "name", Type: ColText, Description: "Gift name"},
		},
	}
	giftNames := []string{"Rose", "Rocket", "Castle", "Dragon", "Galaxy"}
	for g := 1; g <= 5; g++ {
		t.Rows = append(t.Rows, []any{g, giftNames[g-1]})
	}
	return t
}

func giftPriceVersionsTable() *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_gift_price_versions", Entity: "gift_price_versions",
		Description: "Versioned gift prices in stars (virtual asset, not fiat); valid for " +
			"event times in [valid_from, valid_to), NULL valid_to = current",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Price version ID"},
			{Name: "gift_id", Type: ColInt, Description: "Gift ID"},
			{Name: "price_stars", Type: ColBigInt, Description: "Unit price in stars"},
			{Name: "valid_from", Type: ColDate, Description: "First day this version applies"},
			{Name: "valid_to", Type: ColDate, Nullable: true,
				Description: "First day this version no longer applies; NULL = current"},
		},
	}
	id := 0
	for g := 1; g <= 5; g++ {
		id++
		if g == 1 {
			t.Rows = append(t.Rows, []any{id, g, giftStarsPrice(g, 0), day(0), day(splitSwitchDay)})
			id++
			t.Rows = append(t.Rows,
				[]any{id, g, giftStarsPrice(g, splitSwitchDay), day(splitSwitchDay), nil})
			continue
		}
		t.Rows = append(t.Rows, []any{id, g, giftStarsPrice(g, 0), day(0), nil})
	}
	return t
}

func revenueShareRulesTable() *Table {
	return &Table{
		Module: "live-monetization", Name: "wl_revenue_share_rule_versions",
		Entity: "revenue_share_rule_versions",
		Description: "Versioned revenue share rules in basis points; splits must use the " +
			"version valid at gift event time",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Share rule version ID"},
			{Name: "creator_bps", Type: ColInt,
				Description: "Creator share of gift stars in basis points"},
			{Name: "agency_bps", Type: ColInt,
				Description: "Agency share for signed creators in basis points (independent creators: 0)"},
			{Name: "valid_from", Type: ColDate, Description: "First day this version applies"},
			{Name: "valid_to", Type: ColDate, Nullable: true,
				Description: "First day this version no longer applies; NULL = current"},
		},
		Rows: [][]any{
			{1, 5000, 1000, day(0), day(splitSwitchDay)},
			{2, 5500, 1000, day(splitSwitchDay), nil},
		},
	}
}

func giftEventsTable() *Table {
	return &Table{
		Module: "live-monetization", Name: "wl_gift_events", Entity: "gift_events",
		Description: "Gift-sending events in sessions; NOT 1:1 with charges — charge_failed " +
			"events consumed nothing, reversed events were charged then reversed, duplicate " +
			"rows (dedup_of set) are repeat deliveries and must not be double counted",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Gift event row ID"},
			{Name: "session_id", Type: ColInt, Description: "Live session ID"},
			{Name: "creator_id", Type: ColInt, Description: "Receiving creator ID"},
			{Name: "gift_id", Type: ColInt, Description: "Gift ID"},
			{Name: "quantity", Type: ColInt, Description: "Gift count"},
			{Name: "total_stars", Type: ColBigInt,
				Description: "Stars charged (0 for charge_failed and duplicate rows)"},
			{Name: "status", Type: ColCode,
				Description: "processed | charge_failed | reversed | duplicate"},
			{Name: "dedup_of", Type: ColInt, Nullable: true,
				Description: "Original event row ID when this row is a duplicate delivery; NULL otherwise"},
			{Name: "occurred_at", Type: ColDateTime, Description: "Event time"},
		},
	}
}

func revenueSplitsTable() *Table {
	return &Table{
		Module: "live-monetization", Name: "wl_revenue_splits", Entity: "revenue_splits",
		Description: "Revenue split per processed gift event in stars, using the share rule " +
			"version valid at event time; platform_stars = total - creator - agency",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Split ID"},
			{Name: "gift_event_id", Type: ColInt, Description: "Gift event row ID"},
			{Name: "creator_id", Type: ColInt,
				Description: "Receiving creator ID (denormalized from the event)"},
			{Name: "rule_version_id", Type: ColInt, Description: "Share rule version applied"},
			{Name: "creator_stars", Type: ColBigInt, Description: "Creator share in stars"},
			{Name: "agency_stars", Type: ColBigInt,
				Description: "Agency share in stars (0 for independent creators)"},
			{Name: "platform_stars", Type: ColBigInt, Description: "Platform remainder in stars"},
			{Name: "occurred_at", Type: ColDateTime, Description: "Gift event time (denormalized)"},
		},
	}
}

func creatorSettlementsTable(cfg Config) *Table {
	t := &Table{
		Module: "live-monetization", Name: "wl_creator_settlements", Entity: "creator_settlements",
		Description: "Completed creator settlements in stars for a covered period; creator " +
			"income after the period end is pending and NOT included",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Creator settlement ID"},
			{Name: "creator_id", Type: ColInt, Description: "Creator ID"},
			{Name: "period_start", Type: ColDate, Description: "Covered period start (inclusive)"},
			{Name: "period_end", Type: ColDate, Description: "Covered period end (exclusive)"},
			{Name: "settled_stars", Type: ColBigInt, Description: "Settled creator share in stars"},
			{Name: "status", Type: ColCode, Description: "paid"},
			{Name: "settled_at", Type: ColDateTime, Description: "Settlement execution time"},
		},
	}
	settled := map[int]int64{}
	for _, p := range giftPlans(cfg) {
		if p.Status != "processed" || p.Day >= creatorSettleCutoff {
			continue
		}
		settled[p.Creator] += p.Stars * creatorShareBps(p.Day) / 10000
	}
	id := 0
	for c := 1; c <= numCreators; c++ {
		if settled[c] == 0 {
			continue
		}
		id++
		t.Rows = append(t.Rows, []any{
			id, c, day(0), day(creatorSettleCutoff), settled[c], "paid",
			at(creatorSettleCutoff+2, 11, 0, 0),
		})
	}
	return t
}
