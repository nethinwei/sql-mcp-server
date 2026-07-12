package workload

import "fmt"

// Commerce-core reference constants. Applications carry the currency used by
// every downstream money amount of that application.
var (
	orgNames     = []string{"Nova Commerce", "Lumen Media", "Astra Games"}
	orgCountries = []string{"US", "CN", "KR"}

	appNames = []string{"novashop", "novamarket", "lumenlive", "astraplay", "lumenpay"}
	appOrg   = []int{1, 1, 2, 3, 2}
	appCcy   = []string{"USD", "USD", "CNY", "KRW", "CNY"}

	customerCountries = []string{"US", "CN", "KR", "DE", "JP"}
)

const (
	numCustomers = 40
	numUsers     = 30
	numProducts  = 12
	numOrders    = 120 // base count; fact tables scale with Config.Scale
)

// scaledOrders is the effective order count (dimension tables stay fixed;
// fact tables multiply by scale).
func scaledOrders(cfg Config) int { return numOrders * cfg.Scale }

// priceMinorAt returns the unit price (minor units) of product p valid on
// epoch-day offset. Products with an even id get a second price version
// (+100 minor) from 2025-03-01 (day 59); odd products keep one version.
func priceMinorAt(p int, dayOffset int) int64 {
	base := int64(p) * 500
	if p%2 == 0 && dayOffset >= 59 {
		return base + 100
	}
	return base
}

// orderStatus cycles deterministically: 25% cancelled, 25% confirmed,
// 50% completed.
func orderStatus(n int) string {
	switch n % 4 {
	case 0:
		return "cancelled"
	case 1:
		return "confirmed"
	default:
		return "completed"
	}
}

// orderDay maps order n (1-based) onto days 0..180 (2025-01-01 ..
// 2025-06-30); above the base count the day cycle repeats, so any scale
// covers the same period.
func orderDay(n int) int { return ((n - 1) % numOrders) * 181 / numOrders }

func opaqueEmail(seed int64, kind string, n int) string {
	return fmt.Sprintf("u%012x@example.com", pick(seed, kind, "email", n)&0xffffffffffff)
}

func buildCommerce(d *Dataset) {
	d.Tables = append(d.Tables,
		commerceCurrencies(), commerceCountries(), commerceOrganizations(),
		commerceApplications(), commerceUsers(d.Config), commerceCustomers(d.Config),
		commerceProducts(), commercePriceVersions())
	orders, items := commerceOrdersAndItems(d.Config)
	d.Tables = append(d.Tables, orders, items)
}

func commerceCurrencies() *Table {
	return &Table{
		Module: "commerce-core", Name: "wl_currencies", Entity: "currencies",
		Description: "Currencies; minor_unit_scale is the exponent between the minor unit " +
			"stored in *_minor columns and the display unit (2 means 100 minor = 1 unit, " +
			"0 means minor = unit)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Currency ID"},
			{Name: "code", Type: ColCode, Description: "ISO currency code"},
			{Name: "name", Type: ColText, Description: "Currency name"},
			{Name: "minor_unit_scale", Type: ColInt,
				Description: "Decimal places between minor unit and display unit"},
		},
		Rows: [][]any{
			{1, "USD", "US Dollar", 2},
			{2, "CNY", "Chinese Yuan", 2},
			{3, "KRW", "Korean Won", 0},
		},
	}
}

func commerceCountries() *Table {
	return &Table{
		Module: "commerce-core", Name: "wl_countries", Entity: "countries",
		Description: "Countries and their sales region",
		PrimaryKey:  []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Country ID"},
			{Name: "code", Type: ColCode, Description: "ISO country code"},
			{Name: "name", Type: ColText, Description: "Country name"},
			{Name: "region", Type: ColCode, Description: "Sales region: amer | apac | emea"},
		},
		Rows: [][]any{
			{1, "US", "United States", "amer"},
			{2, "CN", "China", "apac"},
			{3, "KR", "South Korea", "apac"},
			{4, "DE", "Germany", "emea"},
			{5, "JP", "Japan", "apac"},
		},
	}
}

func commerceOrganizations() *Table {
	t := &Table{
		Module: "commerce-core", Name: "wl_organizations", Entity: "organizations",
		Description: "Tenant organizations (business owners of applications)",
		PrimaryKey:  []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Organization ID"},
			{Name: "name", Type: ColText, Description: "Organization name"},
			{Name: "country_code", Type: ColCode, Description: "Home country code"},
			{Name: "created_at", Type: ColDate, Description: "Registration date"},
		},
	}
	for i, name := range orgNames {
		t.Rows = append(t.Rows, []any{i + 1, name, orgCountries[i], day(i * 3)})
	}
	return t
}

func commerceApplications() *Table {
	t := &Table{
		Module: "commerce-core", Name: "wl_applications", Entity: "applications",
		Description: "Applications (independently operated products under an organization); " +
			"currency_code is the settlement currency of all money amounts of that application",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Application ID"},
			{Name: "organization_id", Type: ColInt, Description: "Owning organization ID"},
			{Name: "name", Type: ColCode, Description: "Application name"},
			{Name: "currency_code", Type: ColCode, Description: "Settlement currency of this application"},
			{Name: "status", Type: ColCode, Description: "live | paused"},
		},
	}
	for i, name := range appNames {
		status := "live"
		if i == 4 {
			status = "paused"
		}
		t.Rows = append(t.Rows, []any{i + 1, appOrg[i], name, appCcy[i], status})
	}
	return t
}

// commerceUsers are platform login accounts. They are deliberately NOT the
// transacting customers (similar-entity trap).
func commerceUsers(cfg Config) *Table {
	t := &Table{
		Module: "commerce-core", Name: "wl_users", Entity: "users",
		Description: "Platform login accounts (staff and operators); NOT the transacting " +
			"customers — see the customers entity",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "User ID"},
			{Name: "email", Type: ColText, Description: "Login email"},
			{Name: "display_name", Type: ColText, Description: "Display name"},
			{Name: "created_at", Type: ColDate, Description: "Account creation date"},
		},
	}
	for n := 1; n <= numUsers; n++ {
		t.Rows = append(t.Rows,
			[]any{n, opaqueEmail(cfg.Seed, "user", n), fmt.Sprintf("Operator %d", n), day(n)})
	}
	return t
}

func commerceCustomers(cfg Config) *Table {
	t := &Table{
		Module: "commerce-core", Name: "wl_customers", Entity: "customers",
		Description: "Transacting customers of an organization; deleted_at marks logical " +
			"deletion (excluded from active-customer counts)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Customer ID"},
			{Name: "organization_id", Type: ColInt, Description: "Owning organization ID"},
			{Name: "email", Type: ColText, Description: "Contact email (masked)"},
			{Name: "country_code", Type: ColCode, Description: "Customer country code"},
			{Name: "created_at", Type: ColDate, Description: "First-seen date"},
			{Name: "deleted_at", Type: ColDateTime, Nullable: true,
				Description: "Logical deletion time; NULL means active"},
		},
	}
	for n := 1; n <= numCustomers; n++ {
		var deleted any
		if cfg.Anomalies.SoftDeletes && n%13 == 0 {
			deleted = at(150+n%30, 12, 0, 0)
		}
		t.Rows = append(t.Rows, []any{
			n, (n-1)%3 + 1, opaqueEmail(cfg.Seed, "customer", n),
			customerCountries[(n-1)%5], day(n), deleted,
		})
	}
	return t
}

func commerceProducts() *Table {
	t := &Table{
		Module: "commerce-core", Name: "wl_products", Entity: "products",
		Description: "Sellable products; current price lives in product_price_versions " +
			"(never on this table)",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Product ID"},
			{Name: "application_id", Type: ColInt, Description: "Selling application ID"},
			{Name: "name", Type: ColText, Description: "Product name"},
			{Name: "product_type", Type: ColCode, Description: "one_time | subscription | gift_pack"},
			{Name: "status", Type: ColCode, Description: "active | archived"},
		},
	}
	types := []string{"one_time", "subscription", "gift_pack"}
	for n := 1; n <= numProducts; n++ {
		status := "active"
		if n%11 == 0 {
			status = "archived"
		}
		t.Rows = append(t.Rows, []any{
			n, (n-1)%5 + 1, fmt.Sprintf("Product %d", n), types[(n-1)%3], status,
		})
	}
	return t
}

func commercePriceVersions() *Table {
	t := &Table{
		Module: "commerce-core", Name: "wl_product_price_versions", Entity: "product_price_versions",
		Description: "Versioned product prices in minor units; a version is valid for order " +
			"dates in [valid_from, valid_to), open-ended when valid_to is NULL. Historical " +
			"orders keep the price valid at order creation",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Price version ID"},
			{Name: "product_id", Type: ColInt, Description: "Product ID"},
			{Name: "price_minor", Type: ColBigInt, Description: "Unit price in minor units of currency_code"},
			{Name: "currency_code", Type: ColCode,
				Description: "Price currency (application settlement currency)"},
			{Name: "valid_from", Type: ColDate, Description: "First day this version applies"},
			{Name: "valid_to", Type: ColDate, Nullable: true,
				Description: "First day this version no longer applies; NULL = current"},
		},
	}
	id := 0
	for p := 1; p <= numProducts; p++ {
		ccy := appCcy[(p-1)%5]
		id++
		if p%2 == 0 {
			t.Rows = append(t.Rows, []any{id, p, int64(p) * 500, ccy, day(0), day(59)})
			id++
			t.Rows = append(t.Rows, []any{id, p, int64(p)*500 + 100, ccy, day(59), nil})
			continue
		}
		t.Rows = append(t.Rows, []any{id, p, int64(p) * 500, ccy, day(0), nil})
	}
	return t
}

func commerceOrdersAndItems(cfg Config) (*Table, *Table) {
	orders := &Table{
		Module: "commerce-core", Name: "wl_orders", Entity: "orders",
		Description: "Business orders (transaction intent, NOT payment success); money in " +
			"minor units of currency_code. created_at / confirmed_at / cancelled_at / " +
			"completed_at are distinct lifecycle times",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Order ID"},
			{Name: "application_id", Type: ColInt, Description: "Application ID"},
			{Name: "customer_id", Type: ColInt, Description: "Customer ID"},
			{Name: "status", Type: ColCode, Description: "created | confirmed | cancelled | completed"},
			{Name: "currency_code", Type: ColCode,
				Description: "Order currency (application settlement currency)"},
			{Name: "total_minor", Type: ColBigInt,
				Description: "Order total in minor units (sum of items)"},
			{Name: "created_at", Type: ColDateTime, Description: "Order creation time (UTC)"},
			{Name: "confirmed_at", Type: ColDateTime, Nullable: true,
				Description: "Confirmation time; NULL until confirmed"},
			{Name: "cancelled_at", Type: ColDateTime, Nullable: true,
				Description: "Cancellation time; NULL unless cancelled"},
			{Name: "completed_at", Type: ColDateTime, Nullable: true,
				Description: "Fulfilment completion time; NULL until completed"},
		},
	}
	items := &Table{
		Module: "commerce-core", Name: "wl_order_items", Entity: "order_items",
		Description: "Order line items (one row per product in an order — finer grain than " +
			"orders); unit_price_minor is the historical price at order creation",
		PrimaryKey: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: ColInt, Description: "Order item ID"},
			{Name: "order_id", Type: ColInt, Description: "Order ID"},
			{Name: "product_id", Type: ColInt, Description: "Product ID"},
			{Name: "quantity", Type: ColInt, Description: "Quantity"},
			{Name: "unit_price_minor", Type: ColBigInt,
				Description: "Unit price in minor units at order creation"},
			{Name: "currency_code", Type: ColCode, Description: "Item currency"},
		},
	}
	itemID := 0
	for n := 1; n <= scaledOrders(cfg); n++ {
		total := appendOrderItems(items, n, &itemID)
		orders.Rows = append(orders.Rows, orderRow(n, total))
	}
	return orders, items
}

// appendOrderItems adds order n's line items and returns the order total.
func appendOrderItems(items *Table, n int, itemID *int) int64 {
	ccy := appCcy[(n-1)%5]
	var total int64
	for i := 0; i < 1+n%3; i++ {
		product := (n*7+i)%numProducts + 1
		qty := 1 + (n+i)%2
		unit := priceMinorAt(product, orderDay(n))
		*itemID++
		items.Rows = append(items.Rows, []any{*itemID, n, product, qty, unit, ccy})
		total += int64(qty) * unit
	}
	return total
}

func orderRow(n int, total int64) []any {
	created := at(orderDay(n), n%24, (n*7)%60, 0)
	status := orderStatus(n)
	var confirmed, cancelled, completed any
	switch status {
	case "confirmed":
		confirmed = created.Add(oneHour)
	case "completed":
		confirmed = created.Add(oneHour)
		completed = created.Add(48 * oneHour)
	case "cancelled":
		cancelled = created.Add(2 * oneHour)
	}
	return []any{
		n, (n-1)%5 + 1, (n-1)%numCustomers + 1, status, appCcy[(n-1)%5], total,
		created, confirmed, cancelled, completed,
	}
}
