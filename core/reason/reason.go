// Package reason holds the explanations the server gives an administrator:
// why a capability is degraded, why a connection cannot run an action, what
// a configuration relies on. Each has a stable code the console translates
// (zh-CN, en), named parameters, and the English text logs, the CLI and MCP
// show.
package reason

import (
	"slices"
	"strings"
)

// Code names an explanation; its English template is in templates.
type Code string

// Reason is one explanation: its code, the values of its template's
// {placeholders}, and its English text. A placeholder may name a part, an
// explanation of its own (the cause, the work affected) that the console
// translates by its own code; parts do not nest.
type Reason struct {
	Code   Code
	Params map[string]string
	Parts  map[string]Reason
	Text   string
}

// String is the English text.
func (r Reason) String() string { return r.Text }

// New is the explanation code gives with params, name and value pairs
// filling its template's {name} placeholders.
func New(code Code, params ...string) Reason {
	return Compose(code, nil, params...)
}

// Compose is New with parts, explanations filling placeholders of their own
// by their text.
func Compose(code Code, parts map[string]Reason, params ...string) Reason {
	r := Reason{Code: code, Parts: parts}
	pairs := make([]string, 0, len(params)+2*len(parts))
	if len(params) > 1 {
		r.Params = make(map[string]string, len(params)/2)
	}
	for i := 0; i+1 < len(params); i += 2 {
		r.Params[params[i]] = params[i+1]
		pairs = append(pairs, "{"+params[i]+"}", params[i+1])
	}
	for name, part := range parts {
		pairs = append(pairs, "{"+name+"}", part.Text)
	}
	text, ok := templates[code]
	if !ok {
		text = string(code)
	}
	r.Text = strings.NewReplacer(pairs...).Replace(text)
	return r
}

// Codes lists every code, sorted.
func Codes() []Code {
	out := make([]Code, 0, len(templates))
	for c := range templates {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// Template is the English template of code, with {placeholders}.
func Template(code Code) string { return templates[code] }
