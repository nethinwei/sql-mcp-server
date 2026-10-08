package config

import (
	"strings"
	"testing"
)

func connections(conns Connections, routing RoutingConfig) DatabaseConfig {
	return DatabaseConfig{Driver: "postgres", Connections: conns, Routing: routing}
}

func TestValidateConnectionsAndRouting(t *testing.T) {
	t.Parallel()
	rw, ro := ConnectionConfig{DSN: "rw"}, ConnectionConfig{DSN: "ro"}
	replica := ConnectionConfig{DSN: "r", Role: "replica"}
	one := Connections{"rw": rw}
	for name, tc := range map[string]struct {
		db   DatabaseConfig
		want string
	}{
		"neither":       {DatabaseConfig{Driver: "postgres"}, "DSN"},
		"both":          {DatabaseConfig{Driver: "postgres", DSN: "x", Connections: one}, "both"},
		"routing alone": {DatabaseConfig{Driver: "postgres", DSN: "x", Routing: RoutingConfig{Read: "x"}}, "without"},
		"unrouted":      {connections(Connections{"rw": rw, "ro": ro}, RoutingConfig{}), "routing"},
		"unknown":       {connections(one, RoutingConfig{Read: "rw", Write: "rw", Execute: "nope"}), "unknown"},
		"replica writes": {connections(Connections{"rw": rw, "r": replica},
			RoutingConfig{Read: "r", Write: "r", Execute: "rw"}), "replica"},
		"bad role":   {connections(Connections{"rw": {DSN: "x", Role: "standby"}}, RoutingConfig{}), "role"},
		"bad pooler": {connections(Connections{"rw": {DSN: "x", Pooler: "session"}}, RoutingConfig{}), "pooler"},
		"negative":   {DatabaseConfig{Driver: "postgres", DSN: "x", ReadAfterWrite: -1}, "readAfterWrite"},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Config{Databases: map[string]DatabaseConfig{"main": tc.db}}
			c.ApplyDefaults()
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	routed := connections(Connections{"rw": rw, "ro": ro, "r": replica},
		RoutingConfig{Read: "r", Write: "rw", Execute: "rw"})
	routed.ReadAfterWrite = 5e9
	for name, db := range map[string]DatabaseConfig{
		"dsn shorthand":  {Driver: "postgres", DSN: "x"},
		"one connection": connections(one, RoutingConfig{}),
		"routed":         routed,
	} {
		c := &Config{Databases: map[string]DatabaseConfig{"main": db}}
		c.ApplyDefaults()
		if err := c.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	want := RoutingConfig{Read: "default", Write: "default", Execute: "default"}
	if r := (DatabaseConfig{DSN: "x"}).Route(); r != want {
		t.Errorf("shorthand route = %+v", r)
	}
}
