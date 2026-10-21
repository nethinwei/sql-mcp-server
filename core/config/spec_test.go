package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fill sets every exported field of v, recursively, to a value that is not
// zero: a configuration writing everything.
func fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Duration]() {
			return
		}
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i))
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0))
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		el := reflect.New(v.Type().Elem()).Elem()
		fill(el)
		m.SetMapIndex(reflect.ValueOf("k").Convert(v.Type().Key()), el)
		v.Set(m)
	case reflect.Interface:
		v.Set(reflect.ValueOf("v"))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(int64(time.Second))
	case reflect.Float64:
		v.SetFloat(1.5)
	}
}

// Every field of a configuration built in code reaches its Spec and back:
// the Spec of the configuration a Spec means is that Spec.
func TestSpecOfEveryField(t *testing.T) {
	var cfg Config
	fill(reflect.ValueOf(&cfg).Elem())
	spec := SpecOf(&cfg)
	back := spec.config()
	if again := SpecOf(&back); !reflect.DeepEqual(again, spec) {
		t.Fatalf("a field was lost on the way:\n%+v\n%+v", again, spec)
	}
	// A field written is in the Spec; dmlTools is unset unless its flag says
	// it was written (see MCPFlags).
	walkSpec(reflect.ValueOf(spec).Elem(), "", func(path string, v reflect.Value) {
		if v.IsNil() && !strings.HasSuffix(path, ".mcp.dmlTools") {
			t.Errorf("%s is unset though written", path)
		}
	})
}

// walkSpec calls fn with each pointer, list or map field of a Spec.
func walkSpec(v reflect.Value, path string, fn func(string, reflect.Value)) {
	for i := range v.NumField() {
		f, fv := v.Type().Field(i), v.Field(i)
		p := path + "." + f.Tag.Get("yaml")
		switch fv.Kind() {
		case reflect.Pointer:
			fn(p, fv)
			if !fv.IsNil() && fv.Elem().Kind() == reflect.Struct && fv.Elem().Type() != reflect.TypeFor[time.Duration]() {
				walkSpec(fv.Elem(), p, fn)
			}
		case reflect.Slice, reflect.Map:
			fn(p, fv)
		}
	}
}

// zeroSpec sets every pointer of a Spec to a zero value and every list and
// map to an empty one: a configuration writing every zero out.
func zeroSpec(v reflect.Value) {
	for i := range v.NumField() {
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Pointer:
			fv.Set(reflect.New(fv.Type().Elem()))
			if el := fv.Elem(); el.Kind() == reflect.Struct && el.Type() != reflect.TypeFor[time.Duration]() {
				zeroSpec(el)
			}
		case reflect.Slice:
			fv.Set(reflect.MakeSlice(fv.Type(), 0, 0))
		case reflect.Map:
			fv.Set(reflect.MakeMap(fv.Type()))
		}
	}
}

// Canonical unsets exactly the zeros that the configuration omits when it
// writes them: an empty schema, an empty list of params; a zero it writes
// (maxRows: 0) and a switch that defaults on (cost.enabled: false) stay.
func TestCanonicalUnsetsOmittedZeros(t *testing.T) {
	var spec Spec
	zeroSpec(reflect.ValueOf(&spec).Elem())
	spec.Entities = List[EntitySpec]{{}}
	zeroSpec(reflect.ValueOf(&spec.Entities[0]).Elem())
	spec.Canonical()
	e := spec.Entities[0]
	if e.Schema != nil || e.Params != nil || e.AllowCascade != nil || e.FieldACL != nil {
		t.Fatalf("an omitted zero stayed: %+v", e)
	}
	if e.Name == nil || spec.Cost.MaxRows == nil || spec.Cost.Enabled == nil || spec.Tools.ReadRecords == nil {
		t.Fatalf("a written zero was unset: %+v %+v", e, *spec.Cost)
	}
	cfg := spec.config()
	cfg.ApplyDefaults()
	if *cfg.Cost.Enabled || cfg.Cost.MaxRows != 0 || cfg.Tools.ReadRecords {
		t.Fatalf("explicit zeros were defaulted: %+v %+v", cfg.Cost, cfg.Tools)
	}
}

// Resolve fills defaults in and validates: nothing written is the default
// configuration; a written mistake is refused.
func TestResolve(t *testing.T) {
	db := Map[string, DatabaseSpec]{"shop": {Driver: ptr("postgres"), DSN: ptr("x")}}
	cfg, err := Resolve(&Spec{Databases: db})
	if err != nil || cfg.Cost.MaxRows != 10000 || !cfg.Tools.ReadRecords || cfg.Server.Transport != "stdio" {
		t.Fatalf("Resolve(a database only) = %+v, %v", cfg, err)
	}
	if _, err := Resolve(&Spec{Databases: db, Cost: &CostSpec{MaxRows: ptr(int64(-1))}}); err == nil {
		t.Fatal("an invalid configuration resolved")
	}
	if _, err := Resolve(nil); err == nil {
		t.Fatal("a configuration without a database resolved")
	}
	legacy := &DatabaseSpec{Driver: ptr("postgres"), DSN: ptr("y")}
	if _, err := Resolve(&Spec{Database: legacy, Databases: db}); !errors.Is(err, ErrLegacyDatabase) {
		t.Fatalf("Resolve(database and databases) = %v, want ErrLegacyDatabase", err)
	}
	if cfg, err := Resolve(&Spec{Database: legacy}); err != nil || cfg.Databases["default"].DSN != "y" {
		t.Fatalf("Resolve(the legacy database) = %+v, %v", cfg, err)
	}
	// A configuration in effect written out writes both, the same.
	migrated := Map[string, DatabaseSpec]{"default": *legacy}
	if _, err := Resolve(&Spec{Database: legacy, Databases: migrated}); err != nil {
		t.Fatalf("Resolve(the legacy database migrated) = %v", err)
	}
}

func TestListAndMapIsZero(t *testing.T) {
	if !(List[int](nil)).IsZero() || (List[int]{}).IsZero() || !(Map[string, int](nil)).IsZero() ||
		(Map[string, int]{}).IsZero() {
		t.Fatal("only nil is unset")
	}
}

// Resolving a Spec leaves it as written: normalizing the configuration
// changes copies of its lists, never the Spec another caller may share.
func TestResolveLeavesTheSpec(t *testing.T) {
	t.Parallel()
	subjects := List[string]{"DNS:Alice.Example.com"}
	spec := &Spec{
		Databases: Map[string, DatabaseSpec]{"shop": {Driver: ptr("postgres"), DSN: ptr("x")}},
		Users:     Map[string, UserSpec]{"alice": {CertSubjects: subjects, Roles: List[string]{"Reader"}}},
		Roles:     Map[string, RoleDefinitionSpec]{"reader": {}},
	}
	if _, err := Resolve(spec); err != nil {
		t.Fatal(err)
	}
	if subjects[0] != "DNS:Alice.Example.com" || spec.Users["alice"].Roles[0] != "Reader" {
		t.Fatalf("Resolve changed the Spec: %v %v", subjects, spec.Users["alice"].Roles)
	}
}
