package reason

import (
	"encoding/json"
	"flag"
	"os"
	"regexp"
	"slices"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/codes.json")

func TestNewFillsTheTemplate(t *testing.T) {
	t.Parallel()
	r := New(PrivilegeMissing, "privilege", "UPDATE")
	if r.Text != "the account lacks the UPDATE privilege" || r.Params["privilege"] != "UPDATE" || r.String() != r.Text {
		t.Fatalf("New = %+v", r)
	}
	if r := New(UseReads); r.Text != "reads" || r.Params != nil {
		t.Fatalf("New without params = %+v", r)
	}
	if r := New("nope.unknown"); r.Text != "nope.unknown" {
		t.Fatalf("an unknown code reads as itself: %+v", r)
	}
	c := Compose(CapabilityDenied, map[string]Reason{"cause": New(PrivilegeReadOnlyServer)},
		"entity", "orders", "action", "update", "connection", "ro")
	if c.Text != "entity orders: update is granted, but connection ro cannot run it (the server is read-only)" ||
		c.Parts["cause"].Code != PrivilegeReadOnlyServer {
		t.Fatalf("Compose = %+v", c)
	}
}

// TestCodesFixture: testdata/codes.json lists every code and its template;
// the console checks it has zh-CN and en messages for each.
func TestCodesFixture(t *testing.T) {
	t.Parallel()
	all := map[Code]string{}
	placeholder := regexp.MustCompile(`\{([a-zA-Z]+)\}`)
	segment := regexp.MustCompile(`^[a-z][a-zA-Z]*(\.[a-z][a-zA-Z]*)+$`)
	for _, c := range Codes() {
		if !segment.MatchString(string(c)) {
			t.Errorf("code %q is not group.name in lowerCamelCase", c)
		}
		all[c] = Template(c)
		for _, m := range placeholder.FindAllStringSubmatch(Template(c), -1) {
			if slices.Contains([]string{"variable", "value", "error", "privilege", "entity", "action", "connection",
				"datasource", "key", "use", "strength", "cause"}, m[1]) {
				continue
			}
			t.Errorf("code %q has an undocumented placeholder {%s}", c, m[1])
		}
	}
	got, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile("testdata/codes.json", got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/codes.json")
	if err != nil || string(want) != string(got) {
		t.Fatalf("testdata/codes.json is out of date (go test ./core/reason -run TestCodesFixture -update): %v", err)
	}
}
