package mcpquarantine

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	yaml "gopkg.in/yaml.v3"
)

// Credential strips in YAML MCP client files (TENG-3921): Goose
// ~/.config/goose/config.yaml (extensions, keyed by name: envs, headers, args,
// uri) and Continue ~/.continue/config.yaml (mcpServers, a list named by
// "name": env, requestOptions.headers, args, url). Block
// telara-documentation/architecture/estate-remediation.md ("Credential strips").
//
// The file is never re-serialised. yaml.v3 gives each scalar's line and
// column; the scalar's source bytes are found from there and checked by
// decoding them again, and only those bytes are replaced. Plain,
// single-quoted and double-quoted scalars, in block or flow collections, are
// edited this way. A value in any other form (a block scalar, an anchored or
// aliased value, a value reached through a merge key, an explicitly tagged
// value, a plain scalar folded over several lines, an entry with no unique
// name) is still read and matched, and a match is refused as
// credential_unhandled with the file left unchanged.

// yamlMapFields are the server entry fields read as name: value maps. Every
// client's own spelling is listed: env (Continue), envs (Goose), headers
// (Goose), http_headers, and requestOptions.headers (Continue).
var yamlMapFields = []string{"env", "envs", "headers", "http_headers"}

const yamlRequestOptions = "requestOptions"

type yamlDoc struct {
	data       []byte
	lineStarts []int
	out        *spanEntries
}

func yamlSpans(data []byte, itemsKey, idField string) (*spanEntries, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, &n)
	}
	out := &spanEntries{}
	if len(docs) == 0 {
		return out, nil // an empty file
	}
	if len(docs) > 1 {
		return nil, fmt.Errorf("the file holds %d YAML documents; a client config is one", len(docs))
	}
	root := docs[0]
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return out, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the top level is not a mapping")
	}
	d := &yamlDoc{data: data, out: out, lineStarts: []int{0}}
	for i, b := range data {
		if b == '\n' {
			d.lineStarts = append(d.lineStarts, i+1)
		}
	}
	var items *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == "<<" {
			return nil, fmt.Errorf("the top level uses a merge key, so where %s comes from cannot be edited", itemsKey)
		}
		if k.Kind == yaml.ScalarNode && k.Value == itemsKey {
			if items != nil {
				return nil, fmt.Errorf("%s appears twice", itemsKey)
			}
			items = root.Content[i+1]
		}
	}
	if items == nil || (items.Kind == yaml.ScalarNode && items.Tag == "!!null") {
		return out, nil // read whole: no servers
	}
	if items.Kind == yaml.AliasNode || items.Anchor != "" {
		return nil, fmt.Errorf("%s is an anchor or alias, shared with other places in the file", itemsKey)
	}
	switch {
	case idField == "_key" && items.Kind == yaml.MappingNode:
		seen := map[string]bool{}
		for i := 0; i+1 < len(items.Content); i += 2 {
			k := items.Content[i]
			if k.Kind != yaml.ScalarNode || k.Value == "<<" {
				return nil, fmt.Errorf("%s has a key that is not a plain name", itemsKey)
			}
			if seen[k.Value] {
				return nil, fmt.Errorf("%s names %q twice", itemsKey, k.Value)
			}
			seen[k.Value] = true
			d.entry(k.Value, items.Content[i+1], "")
		}
	case idField != "_key" && items.Kind == yaml.SequenceNode:
		names := make([]string, len(items.Content))
		count := map[string]int{}
		for i, e := range items.Content {
			if n := yamlField(e, idField); n != nil && n.Kind == yaml.ScalarNode && n.Value != "" {
				names[i] = n.Value
				count[n.Value]++
			}
		}
		for i, e := range items.Content {
			name, taint := names[i], ""
			switch {
			case name == "":
				name, taint = fmt.Sprintf("#%d", i), "the server entry has no "+idField+", so it cannot be found again to put the value back"
			case count[name] > 1:
				name, taint = fmt.Sprintf("%s#%d", name, i), "two server entries share the "+idField+" "+name+", so it cannot be found again to put the value back"
			}
			d.entry(name, e, taint)
		}
	default:
		return nil, fmt.Errorf("%s is not the shape this client writes", itemsKey)
	}
	return out, nil
}

// yamlField returns the value of key in a mapping node, or nil.
func yamlField(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Kind == yaml.ScalarNode && n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// resolve follows an alias. taint says why a value reached this way cannot be
// edited in place.
func resolve(n *yaml.Node, taint, what string) (*yaml.Node, string) {
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		if taint == "" {
			taint = what + " is an alias of an anchor used elsewhere in the file"
		}
		return n.Alias, taint
	}
	if n.Anchor != "" && taint == "" {
		taint = what + " carries an anchor, so it is shared with every alias of it"
	}
	return n, taint
}

func (d *yamlDoc) entry(server string, n *yaml.Node, taint string) {
	n, taint = resolve(n, taint, "the server entry")
	if n.Kind != yaml.MappingNode {
		return // a server entry with no fields holds no credential
	}
	flow := n.Style&yaml.FlowStyle != 0
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		switch {
		case k.Value == "<<":
			for _, m := range mergeSources(v) {
				d.entry(server, m, "the value comes in through a merge key")
			}
		case k.Value == "args":
			d.args(server, v, taint)
		case isYAMLMapField(k.Value):
			d.mapField(server, k.Value, v, taint)
		case k.Value == yamlRequestOptions:
			opts, t := resolve(v, taint, yamlRequestOptions)
			if h := yamlField(opts, "headers"); h != nil {
				d.mapField(server, yamlRequestOptions+".headers", h, t)
			}
		default:
			// One of the entry's own fields holding a URL with a query.
			if sv, _ := resolve(v, taint, "the value"); sv.Kind == yaml.ScalarNode && len(queryParams(sv.Value)) > 0 {
				d.scalar(server, location{Field: k.Value, Index: -1}, v, flow, taint, false)
			}
		}
	}
}

func isYAMLMapField(k string) bool {
	for _, f := range yamlMapFields {
		if f == k {
			return true
		}
	}
	return false
}

// mergeSources are the mappings a merge key's value names (one alias or a
// list of them).
func mergeSources(v *yaml.Node) []*yaml.Node {
	if v.Kind == yaml.SequenceNode {
		return v.Content
	}
	return []*yaml.Node{v}
}

func (d *yamlDoc) mapField(server, field string, n *yaml.Node, taint string) {
	n, taint = resolve(n, taint, field)
	if n.Kind != yaml.MappingNode {
		return // not a name: value map, so no named credential sits in it
	}
	flow := n.Style&yaml.FlowStyle != 0
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			continue
		}
		if k.Value == "<<" {
			for _, m := range mergeSources(v) {
				d.mapField(server, field, m, "the value comes in through a merge key")
			}
			continue
		}
		d.scalar(server, location{Field: field, Name: k.Value, Index: -1}, v, flow, taint, secretWord.MatchString(k.Value))
	}
}

func (d *yamlDoc) args(server string, n *yaml.Node, taint string) {
	n, taint = resolve(n, taint, "args")
	if n.Kind != yaml.SequenceNode {
		return
	}
	flow := n.Style&yaml.FlowStyle != 0
	prev := ""
	for i, a := range n.Content {
		v, _ := resolve(a, taint, "the value")
		if v.Kind != yaml.ScalarNode {
			prev = ""
			continue
		}
		d.scalar(server, location{Field: "args", Index: i}, a, flow, taint, argSecretNamed(v.Value, prev))
		prev = v.Value
	}
}

// scalar adds one value with its source byte range, or with the reason it has
// none that can be edited.
func (d *yamlDoc) scalar(server string, loc location, n *yaml.Node, flow bool, taint string, secretNamed bool) {
	n, taint = resolve(n, taint, "the value")
	if n.Kind != yaml.ScalarNode || n.Tag == "!!null" {
		return
	}
	c := spanCandidate{candidate: candidate{loc: loc, value: n.Value, secretNamed: secretNamed, unhandled: taint}}
	inner := -1
	if c.unhandled == "" {
		switch {
		case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
			c.unhandled = "the value is a YAML block scalar"
		case n.Style&yaml.TaggedStyle != 0:
			c.unhandled = "the value carries an explicit YAML tag"
		default:
			start, end, why := d.locate(n, flow)
			if why != "" {
				c.unhandled = why
			} else {
				c.start, c.end = start, end
				raw := string(d.data[start:end])
				if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
					inner = quotedInner(raw, n.Value)
				} else if raw == n.Value {
					inner = 0
				}
			}
		}
	}
	d.out.addWithParams(server, c, inner, escapedURLReason)
}

// locate finds a flow scalar's source bytes from its line and column and
// proves them by decoding them again.
func (d *yamlDoc) locate(n *yaml.Node, flow bool) (int, int, string) {
	const lost = "the value's source bytes could not be located exactly (it may be folded over several lines)"
	if n.Line < 1 || n.Line > len(d.lineStarts) || n.Column < 1 {
		return 0, 0, lost
	}
	start := d.lineStarts[n.Line-1]
	for col := 1; col < n.Column; col++ { // yaml.v3 counts columns in characters
		if start >= len(d.data) || d.data[start] == '\n' {
			return 0, 0, lost
		}
		_, size := utf8.DecodeRune(d.data[start:])
		start += size
	}
	end := -1
	switch {
	case n.Style&yaml.DoubleQuotedStyle != 0:
		if start >= len(d.data) || d.data[start] != '"' {
			return 0, 0, lost
		}
		for i := start + 1; i < len(d.data); i++ {
			if d.data[i] == '\\' {
				i++
				continue
			}
			if d.data[i] == '"' {
				end = i + 1
				break
			}
		}
	case n.Style&yaml.SingleQuotedStyle != 0:
		if start >= len(d.data) || d.data[start] != '\'' {
			return 0, 0, lost
		}
		for i := start + 1; i < len(d.data); i++ {
			if d.data[i] == '\'' {
				if i+1 < len(d.data) && d.data[i+1] == '\'' {
					i++
					continue
				}
				end = i + 1
				break
			}
		}
	default:
		end = start
		for end < len(d.data) {
			b := d.data[end]
			if b == '\n' || b == '\r' || (flow && (b == ',' || b == ']' || b == '}')) {
				break
			}
			if b == '#' && end > start && (d.data[end-1] == ' ' || d.data[end-1] == '\t') {
				break
			}
			end++
		}
		for end > start && (d.data[end-1] == ' ' || d.data[end-1] == '\t') {
			end--
		}
	}
	if end <= start {
		return 0, 0, lost
	}
	var check yaml.Node
	if err := yaml.Unmarshal(d.data[start:end], &check); err != nil || len(check.Content) != 1 ||
		check.Content[0].Kind != yaml.ScalarNode || check.Content[0].Value != n.Value {
		return 0, 0, lost
	}
	if strings.ContainsAny(string(d.data[start:end]), "\n\r") && n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) == 0 {
		return 0, 0, lost
	}
	return start, end, ""
}
