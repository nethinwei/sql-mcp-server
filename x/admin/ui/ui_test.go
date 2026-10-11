package ui

import "testing"

func TestWithBasePointsThePageAtTheGatewayPath(t *testing.T) {
	t.Parallel()
	page := []byte(`<head><base href="/admin/" /><script src="./assets/a.js"></script>`)
	if got := string(withBase(page, "/admin/", "/sql-mcp-server/admin/")); got !=
		`<head><base href="/sql-mcp-server/admin/" /><script src="./assets/a.js"></script>` {
		t.Fatalf("page = %s", got)
	}
	if got := string(withBase(page, "/admin/", "/admin/")); got != string(page) {
		t.Fatalf("without a prefix the page changed: %s", got)
	}
}
