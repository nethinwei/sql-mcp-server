package sqladapter

import (
	"fmt"
	"strings"
)

// SessionParams are a DSN's session settings keyed in lower case, as the
// database reads their names (whatever the case): what the DSN set is then
// found however it was written. Two names differing in case alone are an
// error, as which one the database applies last is unknown.
func SessionParams(params map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(params))
	for k, v := range params {
		lower := strings.ToLower(k)
		if _, dup := out[lower]; dup {
			return nil, fmt.Errorf("the dsn sets %q more than once", lower)
		}
		out[lower] = v
	}
	return out, nil
}
