package configapp

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/workspace"
)

// mergeSchema builds the merge rules of items from their types, by path from
// an item's root (see workspace.Schema): a list field tagged merge:"key=p" is
// keyed by its elements' property p; an optional text field (*string), or a
// field tagged merge:"unset" (a switch off unless set), is unset when zero.
// The items of different kinds share one schema, so a path must mean the
// same in each: it panics otherwise.
func mergeSchema(items ...any) workspace.Schema {
	s := workspace.Schema{Lists: map[string]string{}, Unset: map[string]bool{}}
	for _, item := range items {
		walkMergeRules(reflect.TypeOf(item), "", &s)
	}
	return s
}

func walkMergeRules(t reflect.Type, prefix string, s *workspace.Schema) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		rule := f.Tag.Get("merge")
		if f.Type == reflect.TypeFor[*string]() || rule == "unset" {
			s.Unset[path] = true
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.Struct:
			if key, ok := strings.CutPrefix(rule, "key="); ok {
				if was, seen := s.Lists[path+"[]"]; seen && was != key {
					panic(fmt.Sprintf("configapp: %s[] keyed by both %s and %s", path, was, key))
				}
				s.Lists[path+"[]"] = key
			}
			walkMergeRules(ft.Elem(), path+"[]", s)
		case ft.Kind() == reflect.Struct:
			walkMergeRules(ft, path, s)
		}
	}
}
