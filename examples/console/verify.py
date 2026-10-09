"""Checks every feature of the console example on each datasource over MCP.

Run it against the example once it is up: python3 verify.py (SMCP_MCP_URL
sets the endpoint; the tokens are the local demo ones from README.md). It
deletes the rows it creates, but leaves its other changes: it closes ticket
5, sets a stock level and restocks it, and posts ledger entries.
"""
import json
import os
import time
import urllib.request

URL = os.environ.get("SMCP_MCP_URL", "http://127.0.0.1:8080/mcp")
TOKENS = {
    "support": "smcp_sOwYBj4c-yPlpE25X2Ma7UDt5HsHs5PgFC_ow5O27G0",
    "bi": "smcp_MTSZ1FfrtGfi10ECZvMFORLjebhS1RfvYGl2TNgA8_s",
    "finance": "smcp_WwX5CLyiBP4uMCCG8JCrLFPzteuVOaDy37S9KqBJaBc",
    "ops": "smcp_9yrxe6J6_IJZZrzH-BOCuyBFJArH8bxWziaUDJh8GYY",
    "ledger": "smcp_T8f0enbMwGVv40UZ3xpzdM70kwLjfde4VPBgtKL-43Q",
}


class Session:
    def __init__(self, token):
        self.token, self.sid, self.n = token, None, 0
        self.rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                "clientInfo": {"name": "smoke", "version": "1"}})
        self.post({"jsonrpc": "2.0", "method": "notifications/initialized"})
        self.tools = {t["name"] for t in self.rpc("tools/list", {})["tools"]}

    def post(self, body):
        h = {"Authorization": "Bearer " + self.token, "Content-Type": "application/json",
             "Accept": "application/json, text/event-stream"}
        if self.sid:
            h["Mcp-Session-Id"] = self.sid
        r = urllib.request.urlopen(urllib.request.Request(URL, json.dumps(body).encode(), h, method="POST"))
        self.sid = self.sid or r.headers.get("Mcp-Session-Id")
        data = [line[5:] for line in r.read().decode().splitlines() if line.startswith("data:")]
        return json.loads(data[-1]) if data else {}

    def rpc(self, method, params):
        self.n += 1
        return self.post({"jsonrpc": "2.0", "id": self.n, "method": method, "params": params})["result"]

    def call(self, name, args):
        name = name if name in self.tools else next(t for t in self.tools if t.startswith(name))
        res = self.rpc("tools/call", {"name": name, "arguments": args})
        body = res.get("structuredContent") or json.loads(res["content"][0]["text"])
        if isinstance(body, dict) and "result" in body and not res.get("isError"):
            body = body["result"]
        return res.get("isError", False), body


sessions, failures = {}, []


def session(who):
    if who not in sessions:
        sessions[who] = Session(TOKENS[who])
    return sessions[who]


def check(label, ok, detail):
    print(("PASS " if ok else "FAIL ") + label + ("" if ok else "  " + json.dumps(detail, ensure_ascii=False)[:300]))
    if not ok:
        failures.append(label)


def eq(field, value):
    return {"field": field, "op": "eq", "value": value}


def domain(title, d):
    print(f"\n== {title}")
    s = session(d["rls"][0])
    entity, own, other, masked = d["rls"][1:]
    err, body = s.call("read_records", {"entity": entity, "filter": [eq("id", other)]})
    check("tenant policy hides another tenant's row", not err and body == [], body)
    err, body = s.call("read_records", {"entity": entity, "filter": [eq("id", own)]})
    check("reads its own tenant's row", not err and len(body) == 1, body)
    check("masked field", not err and body and "*" in str(body[0].get(masked)), body)
    who, entity, flt, rel = d["expand"]
    err, body = session(who).call("read_records", {"entity": entity, "filter": flt, "expand": [rel]})
    check("expands a relationship", not err and len(body) == 1 and body[0].get(rel), body)

    who, large = d["large"]
    err, body = session(who).call("read_records", {"entity": large})
    check("cost gate refuses an unfiltered read of a large table", err and body.get("code") == "COST_EXCEEDED", body)
    who, entity, group, flt = d["aggregate"]
    err, body = session(who).call("aggregate_records", {"entity": entity, "groupBy": [group], "filter": flt,
                                                        "aggregates": [{"func": "count", "field": group}]})
    check("aggregate", not err and len(body) > 0, body)

    for label, (who, tool, args) in d["shapes"].items():
        err, body = session(who).call(tool, args)
        check(label, not err and body, body)

    who, entity, values = d["create"]
    s = session(who)
    err, body = s.call("create_record", {"entity": entity, "values": values})
    check("create with a database-generated key", not err, body)
    new = body[0].get("lastInsertId") or body[0].get("id") if not err else None
    if new:
        err, body = s.call("read_records", {"entity": entity, "filter": [eq("id", new)]})
        check("reads after a write see it (the write connection)", not err and len(body) == 1, body)
        err, body = s.call("delete_record", {"entity": entity, "filter": [eq("id", new)]})
        check("delete", not err, body)

    who, entity, key, change = d["update"]
    s = session(who)
    err, body = s.call("update_record", {"entity": entity, "filter": key, "set": change})
    check("update by key", not err, body)
    err, body = s.call("begin_transaction", {"datasource": d["datasource"], "readOnly": False})
    check("begin a transaction", not err, body)
    if not err:
        tx = body[0]["transaction"] if isinstance(body, list) else body["transaction"]
        err, body = s.call("update_record", {"entity": entity, "transaction": tx, "filter": key, "set": change})
        check("write in the transaction", not err, body)
        err, body = s.call("commit_transaction", {"transaction": tx})
        check("commit", not err, body)

    who, tool, args, after = d["procedure"]
    entity, flt, field, change = after
    err, body = session(who).call("read_records", {"entity": entity, "filter": flt})
    before = float(body[0][field]) if not err and body and isinstance(change, (int, float)) else None
    err, body = session(who).call(tool, args)
    check("stored procedure as its own tool", not err, body)
    err, body = session(who).call("read_records", {"entity": entity, "filter": flt})
    got = body[0][field] if not err and body else None
    want = change if not isinstance(change, (int, float)) or before is None else before + change
    check("what it changed is read fresh, not from the cache", got is not None and
          (got == want if isinstance(want, str) else abs(float(got) - want) < 1e-6), {"before": before, "after": got})


stamp = int(time.time())
def count_where(who, entity, field, flt):
    return (who, "aggregate_records", {"entity": entity, "filter": flt,
                                        "aggregates": [{"func": "count", "field": field}]})


def read_where(who, entity, flt):
    return (who, "read_records", {"entity": entity, "filter": flt})


ge = lambda field, value: {"field": field, "op": "gte", "value": value}
le = lambda field, value: {"field": field, "op": "lte", "value": value}

domain("shop · PostgreSQL", {
    "datasource": "shop",
    "rls": ("support", "customers", 3, 1, "email"),
    "expand": ("support", "customers", [eq("id", 33)], "tickets"),
    "large": ("bi", "sales.orders"),
    "aggregate": ("bi", "sales.orders", "status", [eq("tenant_id", 1), ge("created_at", "2024-06-01T00:00:00Z")]),
    "shapes": {
        "view": read_where("bi", "open_orders", [eq("id", 3)]),
        "partitioned table, composite key": count_where("bi", "daily_kpi", "day", [ge("day", "2024-12-01")]),
        "same table name in another schema": count_where("bi", "archive.orders", "id", [le("id", 100)]),
    },
    "create": ("finance", "refunds", {"order_id": 9, "amount": 1.5, "reason": "冒烟测试", "status": "requested"}),
    "update": ("support", "support_tickets", [eq("id", 5)], {"priority": "urgent"}),
    "procedure": ("support", "procedure_close_ticket", {"p_ticket": 5},
                  ("support_tickets", [eq("id", 5)], "status", "closed")),
})
domain("warehouse · MySQL", {
    "datasource": "warehouse",
    "rls": ("ops", "logistics.waybills", 3, 1, "receiver_phone"),
    "expand": ("ops", "items", [eq("sku", "SKU-00002")], "stock"),
    "large": ("ops", "archive.waybills"),
    "aggregate": ("ops", "logistics.waybills", "carrier_id", [ge("shipped_at", "2024-12-01T00:00:00Z")]),
    "shapes": {
        "view": read_where("ops", "low_stock", [eq("warehouse_id", 1), eq("sku", "SKU-00036")]),
        "partitioned table, composite key": count_where("ops", "stock_daily", "day", [ge("day", "2024-12-01")]),
        "same table name in another database": count_where("ops", "archive.waybills", "id", [le("id", 100)]),
    },
    "create": ("ops", "logistics.waybills", {"tenant_id": 1, "warehouse_id": 1, "carrier_id": 1,
                                    "tracking_no": f"SMOKE{stamp}", "receiver_phone": "10000000000",
                                    "shipped_at": "2024-06-01 08:00:00"}),
    "update": ("ops", "stock", [eq("warehouse_id", 1), eq("sku", "SKU-00002")], {"quantity": 100}),
    "procedure": ("ops", "procedure_restock", {"p_warehouse": 1, "p_sku": "SKU-00002", "p_quantity": 5},
                  ("stock", [eq("warehouse_id", 1), eq("sku", "SKU-00002")], "quantity", 5)),
})
domain("ledger · OceanBase", {
    "datasource": "ledger",
    "rls": ("ledger", "accounts", 1, 5, "account_no"),
    "expand": ("ledger", "entries", [eq("id", 12)], "account"),
    "large": ("ledger", "entries"),
    "aggregate": ("ledger", "entries", "memo", [eq("account_id", 1), ge("created_at", "2024-12-01T00:00:00Z")]),
    "shapes": {
        "view": read_where("ledger", "account_overview", [eq("account_id", 1)]),
        "partitioned table, composite key": count_where("ledger", "statements", "account_id",
                                                        [ge("period_start", "2024-12-01")]),
        "same table name in another database": count_where("ledger", "archive_entries", "id", [le("id", 100)]),
    },
    "create": ("ledger", "entries", {"tenant_id": 1, "account_id": 1, "amount": 1.5, "memo": "冒烟测试"}),
    "update": ("ledger", "entries", [eq("id", 12)], {"memo": "订单结算（已核对）"}),
    "procedure": ("ledger", "procedure_post_entry", {"p_account": 1, "p_amount": 2.5, "p_memo": "冒烟测试"},
                  ("accounts", [eq("id", 1)], "balance", 2.5)),
})
print("\n== entity names: datasource.schema.name")
names = {}
for who in ("bi", "ops", "ledger"):
    # Each caller can use one tenants entity, so the bare name is enough.
    err, body = session(who).call("read_records", {"entity": "tenants", "filter": [eq("id", 1)]})
    names[who] = body[0]["name"] if not err and body else None
check("a bare name names the one entity the caller can use, each in its own database",
      None not in names.values() and len(set(names.values())) == 3, names)
err, body = session("bi").call("read_records", {"entity": "orders", "filter": [eq("id", 1)]})
check("a name the caller can use twice is ambiguous and lists qualified candidates",
      err and body.get("code") == "AMBIGUOUS_ENTITY" and
      sorted(body.get("constraints", {}).get("candidates", [])) == ["archive.orders", "sales.orders"], body)
err, body = session("bi").call("describe_entities", {})
listed = {e["name"]: e["id"] for e in body} if not err else {}
check("describe names each entity by its shortest unambiguous reference",
      listed.get("sales.orders") == "shop.sales.orders" and listed.get("archive.orders") == "shop.archive.orders" and
      listed.get("tenants") == "shop.crm.tenants", listed)

print(f"\n{len(failures)} failed" if failures else "\nall passed")
raise SystemExit(1 if failures else 0)
