package oauth

import (
	"net/url"
	"strings"
)

// SpawnArgv masks a child process's argument vector for a LOG sink.
//
// The flag names, subcommands, image names, paths and ports survive — an
// operator debugging a spawn still needs to see WHICH flag and WHICH image —
// and only the values are replaced.
//
// Elements that carry embedded whitespace are treated as command STRINGS (the
// `-c` argument of a login-shell wrap) and get the same two rules applied
// token-by-token inside the string, because that is the shape most stdio
// upstreams actually spawn as.
//
// Returns nil for nil.
func (r Redaction) SpawnArgv(argv []string) []string {
	if argv == nil {
		return nil
	}
	return r.argvWith(argv, true, func(s string) string { return r.commandStringTokens(s, true) })
}

func (r Redaction) CommandString(s string) string {
	if s == "" {
		return s
	}
	return r.commandStringTokens(s, false)
}

// commandStringTokens applies argv rules to a command STRING, preserving the
// original whitespace so the line stays readable.
func (r Redaction) commandStringTokens(s string, spawnRules bool) string {
	segs := splitCommandSegments(s)
	tokens := make([]string, 0, len(segs))
	hasRedirect := false
	for _, seg := range segs {
		if seg.isToken {
			tokens = append(tokens, seg.text)
		}
		if seg.isRedirect {
			hasRedirect = true
		}
	}
	if len(tokens) == 0 && !hasRedirect {
		return s
	}
	masked := r.maskTokens(tokens, spawnRules)

	var b strings.Builder
	b.Grow(len(s))
	next := 0
	for _, seg := range segs {
		if seg.isRedirect {
			b.WriteString(r.maskRedirect(seg.text, spawnRules))
			continue
		}
		if !seg.isToken {
			b.WriteString(seg.text)
			continue
		}
		b.WriteString(masked[next])
		next++
	}
	return b.String()
}

// maskTokens applies the flag-name and value-shape rules to a token list, plus
// a second pass over the shell-decoded spelling of each token.
func (r Redaction) maskTokens(tokens []string, spawnRules bool) []string {
	// A command substitution is a command of its own: mask the sensitive
	// flag/value pairs INSIDE it first, so grouping it into one word (as the
	// value of a benign outer flag, a positional arg or a redirect target) does
	// not hide them. Copies; the caller's tokens are never mutated.
	tokens = append([]string(nil), tokens...)
	for i, t := range tokens {
		tokens[i] = r.maskSubstitutions(t, spawnRules)
	}
	masked := r.argvWith(tokens, spawnRules, nil)

	// The flag-name rule reads token TEXT, so a shell-quoted or escaped spelling
	// ('--password' 'a b', --pass\word, "--api"-key) is invisible to it and the
	// value after it would survive. Run the same rules a second time over the
	// shell-DECODED tokens and, wherever that pass masked something, take its
	// rendering (quoting is dropped on a masked token; unmasked tokens keep
	// their original text so the line stays readable).
	decoded := make([]string, len(tokens))
	differs := false
	for i, t := range tokens {
		decoded[i] = shellDecodeToken(t)
		if decoded[i] != t {
			differs = true
		}
	}
	if differs {
		maskedDecoded := r.argvWith(decoded, spawnRules, nil)
		for i := range masked {
			if maskedDecoded[i] != decoded[i] {
				masked[i] = maskedDecoded[i]
			}
		}
	}

	for i, d := range decoded {
		// A word that starts with a dash is a flag (`--password="a b c"`), not a
		// command line: the flat pass already judged it as one unit, and
		// re-splitting its value on whitespace would leave the tail visible.
		if d == tokens[i] || IsArgvFlag(d) {
			continue
		}
		// A value the flat pass already masked WHOLE (the operand of a
		// sensitive flag that merely looks like a command) stays that way:
		// re-parsing it as a command would replace the whole-value mask with
		// partially redacted text that exposes the rest of the secret.
		if mk := r.Mask; masked[i] == mk(d) || masked[i] == mk(tokens[i]) {
			continue
		}
		if inner := r.commandStringTokens(d, spawnRules); inner != d {
			masked[i] = "'" + strings.ReplaceAll(inner, "'", `'\''`) + "'"
		}
	}
	// A NAME=value assignment whose NAME is sensitive (quoted or not) is masked
	// whole: the generic leaf rule misses low-entropy or multiword values
	// (API_KEY=@hunter2xyz, PASSWORD='a b c').
	for i, d := range decoded {
		if name, val, ok := strings.Cut(d, "="); ok && val != "" && isShellVarName(name) &&
			r.envAssignmentNamesSecret(name, spawnRules) {
			masked[i] = name + "=" + r.Mask(val)
		}
	}
	return masked
}

// maskSubstitutions masks the command text inside every `$(...)` and backtick
// substitution of one token (outside single quotes), recursively.
func (r Redaction) maskSubstitutions(t string, spawnRules bool) string {
	if !strings.ContainsAny(t, "$`") {
		return t
	}
	var b strings.Builder
	var quote byte
	ansi := false // inside $'...', where a backslash escapes even a quote
	last := 0
	for j := 0; j < len(t); j++ {
		c := t[j]
		if c == '\\' && (quote != '\'' || ansi) && j+1 < len(t) {
			j++
			continue
		}
		if quote == '\'' {
			if c == '\'' {
				quote, ansi = 0, false
			}
			continue
		}
		switch {
		case c == '$' && quote == 0 && j+1 < len(t) && t[j+1] == '\'':
			quote, ansi = '\'', true
			j++
		case c == '\'' && quote == 0:
			// An apostrophe inside double quotes is literal.
			quote = c
		case c == '"':
			if quote == '"' {
				quote = 0
			} else {
				quote = '"'
			}
		case c == '`' || (c == '$' && j+1 < len(t) && t[j+1] == '('):
			open, bodyStart, closer := j, j+1, byte('`')
			if c == '$' {
				open, bodyStart, closer = j+1, j+2, ')'
			}
			e, ok := scanSubstitution(t, open, closer, true)
			if !ok {
				e, ok = scanSubstitution(t, open, closer, false)
			}
			if !ok {
				// Never closes: mask the rest as a command string.
				b.WriteString(t[last:bodyStart])
				b.WriteString(r.commandStringTokens(t[bodyStart:], spawnRules))
				return b.String()
			}
			b.WriteString(t[last:bodyStart])
			b.WriteString(r.commandStringTokens(t[bodyStart:e-1], spawnRules))
			b.WriteByte(closer)
			last = e
			j = e - 1
		}
	}
	b.WriteString(t[last:])
	return b.String()
}

// maskRedirect redacts the TARGET word of a shell redirection (a filename or a
// here-string body). A redirection is never an argument, so it is excluded from
// flag/value pairing, but its text still reaches the log and can carry a
// credential (`<<< token=...`), so it gets the same value rules as any token.
func (r Redaction) maskRedirect(text string, spawnRules bool) string {
	i := 0
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	for i < len(text) && (isRedirectChar(text[i]) || text[i] == '&' || text[i] == '|') {
		i++
	}
	j := skipCommandSpace(text, i)
	target := text[j:]
	if target == "" {
		return text
	}
	return text[:j] + r.maskTokens([]string{target}, spawnRules)[0]
}

// isShellVarName reports whether s is a valid shell variable name, the left
// side of a `NAME=value` assignment.
func isShellVarName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// envAssignmentNamesSecret applies the same name rule the flag path uses to a
// shell variable name.
func (r Redaction) envAssignmentNamesSecret(name string, spawnRules bool) bool {
	if spawnRules {
		return isSensitiveSpawnFlag(name)
	}
	return IsSensitiveKeyName(name)
}

// shellDecodeToken removes shell quoting and backslash escapes from one command
// token, the way a POSIX shell (bash) would before handing it to the program. It
// is used only to DECIDE what to mask; the display text keeps the original
// spelling. Bash's ANSI-C ($'...') and locale ($"...") quoting forms are decoded
// too: $'--password' is the flag --password.
func shellDecodeToken(t string) string {
	if !strings.ContainsAny(t, "'\"\\") {
		return t
	}
	var b strings.Builder
	var quote byte
	ansi := false
	for i := 0; i < len(t); i++ {
		c := t[i]
		switch {
		case quote == 0 && c == '$' && i+1 < len(t) && t[i+1] == '\'':
			quote, ansi = '\'', true
			i++
		case quote == 0 && c == '$' && i+1 < len(t) && t[i+1] == '"':
			quote = '"'
			i++
		case ansi && c == '\\' && i+1 < len(t):
			i = decodeANSICEscape(t, i, &b)
		case c == '\\' && quote != '\'' && i+1 < len(t) && t[i+1] == '\n':
			i++ // line continuation: the shell deletes the backslash-newline pair
		case c == '\\' && quote != '\'' && i+1 < len(t):
			i++
			b.WriteByte(t[i])
		case quote != 0:
			if c == quote {
				quote, ansi = 0, false
			} else {
				b.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote = c
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// decodeANSICEscape decodes the backslash escape starting at t[i] inside a
// $'...' string, writes the result to b and returns the index of its last byte.
func decodeANSICEscape(t string, i int, b *strings.Builder) int {
	i++ // the escape letter
	switch c := t[i]; c {
	case 'a':
		b.WriteByte(7)
	case 'b':
		b.WriteByte(8)
	case 'e', 'E':
		b.WriteByte(27)
	case 'f':
		b.WriteByte(12)
	case 'n':
		b.WriteByte('\n')
	case 'r':
		b.WriteByte('\r')
	case 't':
		b.WriteByte('\t')
	case 'v':
		b.WriteByte(11)
	case 'x', 'u', 'U':
		max := map[byte]int{'x': 2, 'u': 4, 'U': 8}[c]
		n, j := 0, i+1
		for ; j < len(t) && j-i-1 < max; j++ {
			d := hexVal(t[j])
			if d < 0 {
				break
			}
			n = n*16 + d
		}
		if j == i+1 {
			b.WriteByte('\\')
			b.WriteByte(c)
			return i
		}
		if c == 'x' {
			b.WriteByte(byte(n))
		} else {
			b.WriteRune(rune(n))
		}
		return j - 1
	case '0', '1', '2', '3', '4', '5', '6', '7':
		n, j := 0, i
		for ; j < len(t) && j-i < 3 && t[j] >= '0' && t[j] <= '7'; j++ {
			n = n*8 + int(t[j]-'0')
		}
		b.WriteByte(byte(n))
		return j - 1
	case '\\', '\'', '"', '?':
		b.WriteByte(c)
	default:
		b.WriteByte('\\')
		b.WriteByte(c)
	}
	return i
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

type commandSegment struct {
	text       string
	isToken    bool
	isRedirect bool
}

func isCommandSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// isControlOperator reports whether an UNQUOTED byte starts or continues a shell
// control operator (; & | and their doubled forms). The shell ends a word at one
// even with no whitespace around it, so `:;API_KEY='a b' cmd` assigns API_KEY.
func isControlOperator(b byte) bool { return b == ';' || b == '&' || b == '|' }

// skipCommandSpace returns the index of the first byte at or after i that is
// neither whitespace nor a standalone backslash-newline continuation. The shell
// deletes a continuation before tokenising, so one that sits between words is
// plain whitespace and must not be mistaken for a word of its own.
func skipCommandSpace(s string, i int) int {
	for i < len(s) {
		switch {
		case isCommandSpace(s[i]):
			i++
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '\n':
			i += 2
		default:
			return i
		}
	}
	return i
}

// splitCommandSegments splits a command string into alternating whitespace and
// TOKEN runs so the masked tokens can be spliced back with the original spacing
// intact.
//
// The split is SHELL-AWARE. Command strings may contain quoted arguments
// carrying whitespace:
//
//	npx some-mcp --api-key 'sk live secret'
//
// A whitespace-only split makes that FOUR tokens and the flag-name rule would
// mask only the first fragment (`'sk`). Tracking quote state keeps the whole
// quoted run as one token, so the mask covers all of it.
//
// Shellescape also emits the `'"'"'` idiom for an embedded single quote; the
// state machine below handles it for free (close-single, open-double, literal
// quote, close-double, open-single).
//
// UNBALANCED quotes fall back to the whitespace-only split. An apostrophe in a
// path (`/home/o'brien/x`) would otherwise swallow the rest of the line into
// one token, hiding a following `--api-key SECRET` from the flag-name rule;
// the whitespace split is strictly safer than one giant token.
func splitCommandSegments(s string) []commandSegment {
	segs, ok := splitCommandSegmentsQuoted(s)
	if !ok {
		return splitCommandSegmentsOnSpace(s)
	}
	return segs
}

// splitCommandSegmentsQuoted is the quote-aware split. It reports false when a
// quote is never closed, in which case the caller falls back.
func splitCommandSegmentsQuoted(s string) (segs []commandSegment, balanced bool) {
	return splitCommandSegmentsMode(s, true)
}

// splitCommandSegmentsOnSpace is the whitespace-only split, the fallback for
// command strings whose quoting does not balance. It still
// treats control operators, parentheses and redirections as the shell does,
// but ignores quotes and backslashes.
func splitCommandSegmentsOnSpace(s string) []commandSegment {
	segs, _ := splitCommandSegmentsMode(s, false)
	return segs
}

// isSubshellParen reports whether b is an UNQUOTED subshell parenthesis. The
// shell ends a word at one, so `(API_KEY='a b' cmd)` assigns API_KEY.
func isSubshellParen(b byte) bool { return b == '(' || b == ')' }

func isRedirectChar(b byte) bool { return b == '<' || b == '>' }

// isWordBreak reports whether an unquoted byte ends the current word.
func isWordBreak(b byte) bool {
	return isCommandSpace(b) || isControlOperator(b) || isSubshellParen(b) || isRedirectChar(b)
}

// scanShellWord returns the end of the word starting at i. With quoteAware it
// tracks quotes and backslashes and reports false when a quote is never closed.
func scanShellWord(s string, i int, quoteAware bool) (end int, balanced bool) {
	j := i
	var quote byte // 0 when outside quotes, else the opening quote char
	ansi := false  // inside $'...', where a backslash escapes even in single quotes
	for j < len(s) {
		c := s[j]
		if quoteAware {
			if quote == 0 && c == '$' && j+1 < len(s) && s[j+1] == '\'' {
				quote, ansi = '\'', true
				j += 2
				continue
			}
			// A backslash escapes the next byte unless it sits inside single
			// quotes (where it is literal). Without this, `hunter2\ and\ more`
			// splits into three tokens and only the first is masked.
			if c == '\\' && (quote != '\'' || ansi) && j+1 < len(s) {
				j += 2
				continue
			}
			// A substitution inside double quotes has its OWN quote context: the
			// quotes within it do not close the outer one.
			if quote == '"' && (c == '`' || (c == '$' && j+1 < len(s) && s[j+1] == '(')) {
				open := j
				closer := byte(')')
				if c == '`' {
					closer = '`'
				} else {
					open = j + 1
				}
				e, ok := scanSubstitution(s, open, closer, true)
				if !ok {
					return len(s), false
				}
				j = e
				continue
			}
			if quote != 0 {
				if c == quote {
					quote, ansi = 0, false
				}
				j++
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				j++
				continue
			}
		}
		if quote == 0 && c == '$' && j+1 < len(s) && s[j+1] == '(' {
			e, ok := scanSubstitution(s, j+1, ')', quoteAware)
			if !ok {
				// Never closes: quote-aware callers fall back; the fallback
				// swallows the rest as one word (over-masks, never leaks).
				return len(s), !quoteAware
			}
			j = e
			continue
		}
		if quote == 0 && c == '`' {
			e, ok := scanSubstitution(s, j, '`', quoteAware)
			if !ok {
				// Never closes: quote-aware callers fall back; the fallback
				// swallows the rest as one word (over-masks, never leaks).
				return len(s), !quoteAware
			}
			j = e
			continue
		}
		if isWordBreak(c) {
			break
		}
		j++
	}
	return j, quote == 0
}

// scanSubstitution returns the index just past a command substitution whose
// opener is at s[i] ('(' of `$(`, or a backtick). The whole substitution is part
// of the surrounding word: `--password $(printf %s hunter2)` has ONE value, and
// splitting it on the spaces inside would leave the secret fragments unmasked.
// Nested substitutions, parentheses and quotes are balanced. ok is false when it
// never closes.
func scanSubstitution(s string, i int, closer byte, quoteAware bool) (end int, ok bool) {
	depth := 0
	var quote byte
	ansi := false // inside $'...', where a backslash escapes even a quote
	for j := i; j < len(s); j++ {
		c := s[j]
		if c == '\\' && (quote != '\'' || ansi) && j+1 < len(s) {
			j++
			continue
		}
		if quoteAware && quote == 0 && c == '$' && j+1 < len(s) && s[j+1] == '\'' {
			quote, ansi = '\'', true
			j++
			continue
		}
		if quoteAware && quote != 0 {
			if quote == '"' && (c == '`' || (c == '$' && j+1 < len(s) && s[j+1] == '(')) {
				open, cl := j, byte(')')
				if c == '`' {
					cl = '`'
				} else {
					open = j + 1
				}
				e, ok := scanSubstitution(s, open, cl, true)
				if !ok {
					return len(s), false
				}
				j = e - 1
				continue
			}
			if c == quote {
				quote, ansi = 0, false
			}
			continue
		}
		switch {
		case quoteAware && (c == '\'' || c == '"'):
			quote = c
		case closer == '`':
			if c == '`' && j > i {
				return j + 1, true
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return len(s), false
}

// redirectionEnd reports the end of a shell redirection starting at s[i]: an
// optional file-descriptor number, the operator (> >> < << <<< >& <& >| <>) and
// its target word. The shell strips the whole thing before building argv, so it
// is never an argument and must not be mistaken for the value of a sensitive
// flag. ok is false when s[i] does not start one or its target is unbalanced.
func redirectionEnd(s string, i int, quoteAware bool) (end int, isRedirect, balanced bool) {
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j >= len(s) || !isRedirectChar(s[j]) {
		return i, false, true
	}
	for j < len(s) && isRedirectChar(s[j]) {
		j++
	}
	if j < len(s) && (s[j] == '&' || s[j] == '|') {
		j++
	}
	k := skipCommandSpace(s, j)
	if k >= len(s) || isControlOperator(s[k]) || isSubshellParen(s[k]) || isRedirectChar(s[k]) {
		return j, true, true // no target word (e.g. `>(`, or end of string)
	}
	end, balanced = scanShellWord(s, k, quoteAware)
	if end == k {
		return j, true, balanced
	}
	return end, true, balanced
}

func splitCommandSegmentsMode(s string, quoteAware bool) (segs []commandSegment, balanced bool) {
	segs = make([]commandSegment, 0, 8)
	i := 0
	for i < len(s) {
		j := skipCommandSpace(s, i)
		if j > i {
			segs = append(segs, commandSegment{text: s[i:j]})
			i = j
		}
		if i >= len(s) {
			break
		}
		if isControlOperator(s[i]) || isSubshellParen(s[i]) {
			for j = i; j < len(s) && (isControlOperator(s[j]) || isSubshellParen(s[j])); j++ {
			}
			segs = append(segs, commandSegment{text: s[i:j]})
			i = j
			continue
		}
		if end, isRedir, ok := redirectionEnd(s, i, quoteAware); isRedir {
			if !ok {
				return nil, false
			}
			// Operator and target are not arguments (so they never pair with a
			// flag), but their text is still redacted by value.
			segs = append(segs, commandSegment{text: s[i:end], isRedirect: true})
			i = end
			continue
		}
		j, ok := scanShellWord(s, i, quoteAware)
		if !ok {
			// Ran off the end inside a quote.
			return nil, false
		}
		if j > i {
			segs = append(segs, commandSegment{text: s[i:j], isToken: true})
		}
		i = j
	}
	return segs, true
}

// URLValueDeep renders a URL for a LOG sink, masking credentials in its own
// query string AND inside any query-parameter value that is itself a URL.
//
// The OAuth authorization URL can carry the configured upstream URL verbatim
// as the RFC 8707 `resource` parameter (autoDetectResource's fallback), and it
// is logged and printed on every login attempt. The nested URL arrives
// percent-encoded (`resource=https%3A%2F%2Fhost%2Fmcp%3Ftoken%3DSECRET`), where
// neither the sensitive-query-parameter name rule (which sees one opaque value
// under the name `resource`) nor secretPattern (which needs a literal `=` after
// `token`) can see the credential. Decoding one level is what closes it.
func (r Redaction) URLValueDeep(rawURL string) string {
	return r.urlValueDeep(rawURL, 2)
}

func (r Redaction) urlValueDeep(rawURL string, depth int) string {
	if rawURL == "" {
		return rawURL
	}
	main, fragment, hasFragment := strings.Cut(rawURL, "#")
	base, rawQuery, hasQuery := strings.Cut(main, "?")
	if hasQuery && depth > 0 {
		parts := strings.Split(rawQuery, "&")
		changed := false
		for i, part := range parts {
			key, value, ok := strings.Cut(part, "=")
			if !ok || value == "" {
				continue
			}
			decoded, err := url.QueryUnescape(value)
			if err != nil || !strings.Contains(decoded, "://") {
				continue
			}
			inner := r.urlValueDeep(decoded, depth-1)
			if inner == decoded {
				continue
			}
			parts[i] = key + "=" + escapeNestedURL(inner)
			changed = true
		}
		if changed {
			rawURL = base + "?" + strings.Join(parts, "&")
			if hasFragment {
				rawURL += "#" + fragment
			}
		}
	}
	return r.URLValue(rawURL)
}

var nestedURLEscaper = strings.NewReplacer("&", "%26", "#", "%23", " ", "%20")

func escapeNestedURL(s string) string { return nestedURLEscaper.Replace(s) }

// ExtraParamValue renders one OAuth extra-parameter VALUE for a LOG field.
//
// The `resource` parameter can carry the configured upstream URL verbatim,
// credentials included, so it is rendered through URLValueDeep.
func (r Redaction) ExtraParamValue(key, value string) string {
	if isResourceParam(key) {
		return r.URLValueDeep(value)
	}
	return r.Leaf(key, value)
}

// benignFlagQualifiers are flag-name SEGMENTS that make a marker word
// non-secret.
//
// IsSensitiveKeyName matches its markers as SUBSTRINGS — that is the right
// rule for an env-var name, where over-masking a value costs nothing anyone
// reads. Applying it to a spawn argv on the LOG path is different: the flag's
// VALUE is often the diagnostic the operator opened the log for, and the
// substring rule blanks it for
//
//	--max-tokens 4096          (TOKENS contains TOKEN)
//	--public-key-path /etc/x   (KEY)
//	--num-tokens, --token-limit, --output-tokens, --public-key
//
// none of which is a credential. Flag names, hosts and ordinary values must
// survive for the log to stay useful.
//
// The rule is therefore: de-dash the flag, split it on `-`, `_` and `.`, and if
// any segment is one of these qualifiers, the flag does not name a secret.
//
// A plain segment-EQUALITY rule (segment == marker) would un-mask `--apikey`,
// `--authtoken` and `--clientsecret`, the glued spellings; keeping the
// substring match and subtracting a short, explicit list of qualifiers loses
// no coverage.
var benignFlagQualifiers = map[string]bool{
	"max":    true,
	"min":    true,
	"num":    true,
	"count":  true,
	"total":  true,
	"limit":  true,
	"budget": true,
	"input":  true,
	"output": true,
	"public": true,
	"window": true,
	"size":   true,
}

// isSensitiveSpawnFlag reports whether the token FOLLOWING this flag should be
// masked on a LOG sink. See benignFlagQualifiers.
//
// It is deliberately not used by Redaction.Argv: that function backs the READ
// doors and CheckArgvMaskEcho on the WRITE doors, where relaxing what counts as
// sensitive would newly publish a value on `GET /api/v1/servers`. The
// relaxation is log-only, in the same way the whole spawn rule is.
func isSensitiveSpawnFlag(flag string) bool {
	key := ArgvFlagKey(flag)
	if key == "" {
		return false
	}
	if !IsSensitiveKeyName(key) {
		return false
	}
	for _, seg := range strings.FieldsFunc(key, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		if benignFlagQualifiers[strings.ToLower(seg)] {
			return false
		}
	}
	return true
}

// spawnLeaf is Leaf with the log-only flag relaxation applied to the NAME rule.
// Used for the inline `--max-tokens=4096` spelling, so it stays in step with
// the space-separated one — the invariant Redaction.Argv's own doc comment
// insists on.
func (r Redaction) spawnLeaf(flagKey, value string) string {
	if flagKey != "" && IsSensitiveKeyName(flagKey) && !isSensitiveSpawnFlag(flagKey) {
		// The name rule would have fired on a marker inside a QUALIFIED flag
		// name. Judge the value on its own shape instead — the detector still
		// runs, so `--max-tokens mcp_agt_…` is still masked.
		return r.finish(value, RedactSensitiveData(value))
	}
	return r.Leaf(flagKey, value)
}
