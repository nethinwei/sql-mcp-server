package bootstrap

import (
	"errors"
	"net/url"
	"sort"
	"strings"
)

// dsnSecret is a password inside a DSN: [start, end) spans its raw text
// (quotes included) and value is what the driver reads.
type dsnSecret struct {
	start, end int
	value      string
}

// dsnSecrets returns the passwords in dsn, parsed with the grammar of
// driver's connection strings, so the store secret rule and redaction see
// what the driver sees. A DSN the driver grammar rejects, or one for an
// unknown driver, is scanned with every grammar so that nothing is missed.
func dsnSecrets(driver, dsn string) []dsnSecret {
	if wholePlaceholderRe.MatchString(dsn) {
		return nil
	}
	var (
		out []dsnSecret
		err error
	)
	switch driver {
	case "postgres":
		if isURI(dsn) {
			out, err = uriSecrets(dsn)
		} else {
			out, err = keywordSecrets(dsn, false)
		}
	case "mysql", "oceanbase":
		out, err = mysqlSecrets(dsn)
	default:
		err = errors.New("unknown driver")
	}
	if err == nil {
		return out
	}
	return anySecrets(dsn)
}

func anySecrets(dsn string) []dsnSecret {
	var out []dsnSecret
	if strings.Contains(dsn, "://") {
		s, _ := uriSecrets(dsn)
		out = append(out, s...)
	} else if s, err := mysqlSecrets(dsn); err == nil {
		out = append(out, s...)
	}
	s, _ := keywordSecrets(dsn, true)
	return append(out, s...)
}

func isURI(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// indexOutside returns the first index at or after from of a byte in chars
// that is not inside a ${...} placeholder, or -1.
func indexOutside(s, chars string, from int) int {
	for i := from; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "${") {
			if end := strings.IndexByte(s[i:], '}'); end >= 0 {
				i += end
				continue
			}
		}
		if strings.IndexByte(chars, s[i]) >= 0 {
			return i
		}
	}
	return -1
}

// uriSecrets reads scheme://user:password@host/...?password=...
func uriSecrets(dsn string) ([]dsnSecret, error) {
	start := strings.Index(dsn, "://")
	if start < 0 {
		return nil, errors.New("not a URI")
	}
	start += len("://")
	end := indexOutside(dsn, "/?#", start)
	if end < 0 {
		end = len(dsn)
	}
	var out []dsnSecret
	if at := strings.LastIndexByte(dsn[start:end], '@'); at >= 0 {
		if colon := strings.IndexByte(dsn[start:start+at], ':'); colon >= 0 {
			s, e := start+colon+1, start+at
			out = append(out, dsnSecret{start: s, end: e, value: dsn[s:e]})
		}
	}
	q := indexOutside(dsn, "?", start)
	if q < 0 {
		return out, nil
	}
	for pos := q + 1; pos < len(dsn); {
		next := indexOutside(dsn, "&#", pos)
		if next < 0 {
			next = len(dsn)
		}
		key, _, found := strings.Cut(dsn[pos:next], "=")
		// The driver decodes query keys: %70assword is password.
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if found && isPasswordKey(key) {
			s := pos + strings.IndexByte(dsn[pos:next], '=') + 1
			out = append(out, dsnSecret{start: s, end: next, value: dsn[s:next]})
		}
		if next == len(dsn) || dsn[next] == '#' {
			break
		}
		pos = next + 1
	}
	return out, nil
}

// mysqlSecrets reads [user[:password]@][net[(addr)]]/dbname[?params], with
// the password ending at the last '@' before the last '/', as the driver
// parses it.
func mysqlSecrets(dsn string) ([]dsnSecret, error) {
	slash := strings.LastIndexByte(dsn, '/')
	if slash < 0 {
		return nil, errors.New("no database part")
	}
	at := strings.LastIndexByte(dsn[:slash], '@')
	if at < 0 {
		return nil, nil
	}
	colon := strings.IndexByte(dsn[:at], ':')
	if colon < 0 {
		return nil, nil
	}
	return []dsnSecret{{start: colon + 1, end: at, value: dsn[colon+1 : at]}}, nil
}

// keywordSecrets reads libpq keyword/value settings: spaces may surround
// '=', and a value is either single-quoted or ends at whitespace, with
// backslash escapes in both. tolerant stops at the first syntax error instead
// of failing.
func keywordSecrets(dsn string, tolerant bool) ([]dsnSecret, error) {
	var out []dsnSecret
	for i := skipSpace(dsn, 0); i < len(dsn); i = skipSpace(dsn, i) {
		keyEnd := i
		for keyEnd < len(dsn) && dsn[keyEnd] != '=' && !isSpace(dsn[keyEnd]) {
			keyEnd++
		}
		key := dsn[i:keyEnd]
		eq := skipSpace(dsn, keyEnd)
		if key == "" || eq >= len(dsn) || dsn[eq] != '=' {
			if tolerant {
				return out, nil
			}
			return nil, errors.New("invalid keyword/value setting")
		}
		start := skipSpace(dsn, eq+1)
		end, value, err := keywordValue(dsn, start)
		if err != nil {
			if tolerant {
				return out, nil
			}
			return nil, err
		}
		if isPasswordKey(key) {
			out = append(out, dsnSecret{start: start, end: end, value: value})
		}
		i = end
	}
	return out, nil
}

func keywordValue(dsn string, start int) (end int, value string, err error) {
	var b strings.Builder
	quoted := start < len(dsn) && dsn[start] == '\''
	i := start
	if quoted {
		i++
	}
	for ; i < len(dsn); i++ {
		c := dsn[i]
		switch {
		case c == '\\' && i+1 < len(dsn):
			i++
			b.WriteByte(dsn[i])
		case quoted && c == '\'':
			return i + 1, b.String(), nil
		case !quoted && isSpace(c):
			return i, b.String(), nil
		default:
			b.WriteByte(c)
		}
	}
	if quoted {
		return 0, "", errors.New("unterminated quoted value")
	}
	return len(dsn), b.String(), nil
}

func isPasswordKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "password" || key == "pwd"
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// RedactDSN returns dsn with every password the driver would read replaced
// by ***, for logging and display.
func RedactDSN(driver, dsn string) string {
	secrets := dsnSecrets(driver, dsn)
	sort.Slice(secrets, func(i, j int) bool { return secrets[i].start > secrets[j].start })
	out, limit := dsn, len(dsn)
	for _, s := range secrets {
		if s.end > limit {
			continue // overlaps one already replaced
		}
		out = out[:s.start] + "***" + out[s.end:]
		limit = s.start
	}
	return out
}
