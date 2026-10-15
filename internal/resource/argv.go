package resource

import (
	"fmt"
	"strings"
)

// SplitArgv does POSIX-ish shell word splitting: whitespace-separated
// tokens, single quotes are literal, double quotes allow backslash escapes,
// and a bare backslash outside quotes escapes the next rune. It deliberately
// does NOT support pipes, redirects, or globbing — commands run as a plain
// argv via os/exec, never through a shell. Scripts needing shell features
// should invoke a shell explicitly, e.g. `sh -c "a | b"`.
func SplitArgv(s string) ([]string, error) {
	var (
		args    []string
		cur     strings.Builder
		hasCur  bool
		inSingle, inDouble bool
	)

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			switch r {
			case '"':
				inDouble = false
			case '\\':
				if i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
					i++
					cur.WriteRune(runes[i])
				} else {
					cur.WriteRune(r)
				}
			default:
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
			hasCur = true
		case r == '"':
			inDouble = true
			hasCur = true
		case r == '\\':
			if i+1 < len(runes) {
				i++
				cur.WriteRune(runes[i])
				hasCur = true
			}
		case r == ' ' || r == '\t' || r == '\n':
			if hasCur {
				args = append(args, cur.String())
				cur.Reset()
				hasCur = false
			}
		default:
			cur.WriteRune(r)
			hasCur = true
		}
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in command: %s", s)
	}
	if hasCur {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}
