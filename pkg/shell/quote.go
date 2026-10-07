package shell

import "strings"

// special are the characters a shell (bash, zsh or fish) gives meaning to:
// whitespace, quotes, expansion, globbing, operators, comments, history.
const special = " \t'\"$`\\;&|()<>*?[]{}!#=%^"

// Arg makes s safe to paste as one argument into bash, zsh or fish: as it
// is when nothing in it is special, otherwise in single quotes ('\” for a
// quote, which all three read the same way). ok is false when s holds a
// control character: no quoting makes that safe to paste (a newline ends
// the command, a pasted tab can trigger completion), so callers should
// describe what to do rather than print a command.
func Arg(s string) (quoted string, ok bool) {
	if s == "" {
		return "''", true
	}
	if strings.ContainsFunc(s, isControl) {
		return "", false
	}
	if !strings.ContainsAny(s, special) && !strings.HasPrefix(s, "~") {
		return s, true
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'", true
}

// Path is Arg for a path, leaving a leading ~/ outside the quotes so the
// shell still expands it (~/'my app' works in all three).
func Path(p string) (string, bool) {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		q, ok := Arg(rest)
		return "~/" + q, ok
	}
	return Arg(p)
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) }
