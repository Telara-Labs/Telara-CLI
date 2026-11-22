package mcpquarantine

import (
	"net/url"
	"regexp"
	"strings"
)

// Credentials in URL query strings (TENG-3921). Block
// telara-documentation/architecture/estate-remediation.md ("Credential strips").
//
// Every string a strip reads (a server's own URL-valued fields, and every env,
// header and args value) that is an absolute URL with a query is split into its
// query parameters. Each parameter is a candidate of its own, judged by the
// same rules as any other value: its decoded value against a listed
// fingerprint, or its name against the secret-name rule when the same server
// entry (this URL's query included) carries a listed client id. A strip edits
// only the parameter value's own bytes, so the URL's encoding is kept exactly.

var absoluteURL = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// queryParam is one name=value pair of a URL's query, with the byte range of
// the value (still encoded) in the URL string.
type queryParam struct {
	name       string // decoded
	n          int    // occurrence of this name in the query, from 0
	start, end int
	value      string // decoded; the raw bytes when they do not decode
}

// queryParams lists every name=value pair of v's query, in order. A pair with
// no "=" carries no value and is not listed. Only "&" separates pairs, as Go's
// net/url and WHATWG URL parsing do.
func queryParams(v string) []queryParam {
	if !absoluteURL.MatchString(v) {
		return nil
	}
	q := strings.IndexByte(v, '?')
	if q < 0 {
		return nil
	}
	end := len(v)
	if h := strings.IndexByte(v, '#'); h >= 0 {
		if h < q {
			return nil // the "?" is inside the fragment
		}
		end = h
	}
	var out []queryParam
	seen := map[string]int{}
	for pos := q + 1; pos <= end; {
		segEnd := end
		if amp := strings.IndexByte(v[pos:end], '&'); amp >= 0 {
			segEnd = pos + amp
		}
		seg := v[pos:segEnd]
		if eq := strings.IndexByte(seg, '='); eq > 0 {
			name, err := url.QueryUnescape(seg[:eq])
			if err != nil {
				name = seg[:eq]
			}
			vs := pos + eq + 1
			value, err := url.QueryUnescape(v[vs:segEnd])
			if err != nil {
				value = v[vs:segEnd]
			}
			out = append(out, queryParam{name: name, n: seen[name], start: vs, end: segEnd, value: value})
			seen[name]++
		}
		pos = segEnd + 1
	}
	return out
}

// secretParamName is the secret-name rule for a query parameter: the same rule
// env and header names use, plus "key" (as in ?key=AIza...), which is a secret
// only as a query parameter name.
func secretParamName(name string) bool {
	return secretWord.MatchString(name) || strings.EqualFold(name, "key")
}

// argSecretNamed is the secret-name rule for one argument: its own KEY= or the
// --token style flag before it. A URL argument is never read as KEY=VALUE (its
// query parameters are candidates of their own).
func argSecretNamed(s, prev string) bool {
	if k, _, found := strings.Cut(s, "="); found && !absoluteURL.MatchString(s) && secretWord.MatchString(k) {
		return true
	}
	return strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") && secretWord.MatchString(prev)
}

func (l location) withParam(p queryParam) location {
	l.Param, l.ParamN = p.name, p.n
	return l
}

// appendWithParams appends c and, when c is a URL with a query, one candidate
// per query parameter.
func appendWithParams(out []candidate, c candidate) []candidate {
	out = append(out, c)
	ref := len(out)
	for _, p := range queryParams(c.value) {
		out = append(out, candidate{loc: c.loc.withParam(p), value: p.value, secretNamed: secretParamName(p.name),
			parentRef: ref, sub: [2]int{p.start, p.end}, unhandled: c.unhandled})
	}
	return out
}

// addWithParams is appendWithParams for an in-place file. inner is the offset
// of the decoded value inside the value's source bytes when they are equal
// (no escapes, one line), else -1 with why saying so: a parameter of such a
// value cannot be placed byte for byte and is refused if it matches.
func (e *spanEntries) addWithParams(server string, c spanCandidate, inner int, why string) {
	e.add(server, c)
	ref := len(e.entries[server])
	for _, p := range queryParams(c.value) {
		pc := spanCandidate{candidate: candidate{loc: c.loc.withParam(p), value: p.value, secretNamed: secretParamName(p.name),
			parentRef: ref, sub: [2]int{p.start, p.end}, unhandled: c.unhandled}, raw: true}
		if pc.unhandled == "" {
			if inner < 0 {
				pc.unhandled = why
			} else {
				pc.start, pc.end = c.start+inner+p.start, c.start+inner+p.end
			}
		}
		e.add(server, pc)
	}
}

// quotedInner is the offset of the decoded value inside raw when raw is a
// one-line quoted string whose content is the value itself, else -1.
func quotedInner(raw, value string) int {
	if len(raw) < 2 || (raw[0] != '"' && raw[0] != '\'') || raw[len(raw)-1] != raw[0] {
		return -1
	}
	if strings.HasPrefix(raw, `"""`) || strings.HasPrefix(raw, `'''`) || raw[1:len(raw)-1] != value {
		return -1
	}
	return 1
}

const escapedURLReason = "the URL is written with escapes or across lines, so its query cannot be edited byte for byte"
