package app

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// A selector is one metric a PromQL expression reads, with its label
// matchers. Functions, aggregations, label lists and ranges are not
// selectors.
type selector struct {
	name     string
	matchers []matcher
}

type matcher struct {
	label, op, value string
}

// The words that are PromQL syntax rather than metric names. A modifier that
// takes a label list is followed by one in parentheses, which is skipped.
var (
	promqlListModifiers = map[string]bool{
		"by": true, "without": true, "on": true, "ignoring": true, "group_left": true, "group_right": true,
	}
	promqlKeywords = map[string]bool{
		"and": true, "or": true, "unless": true, "bool": true, "offset": true, "atan2": true,
		"inf": true, "nan": true,
	}
)

type tokenKind int

const (
	tokenIdent tokenKind = iota
	tokenString
	tokenNumber
	tokenMatchers
	tokenPunct
)

type token struct {
	kind     tokenKind
	text     string
	matchers []matcher
}

// promqlSelectors lists the metrics expr reads. It is a scanner, not a full
// parser: it knows enough PromQL to tell a metric from a function, a keyword,
// a label list or a range, and fails on what it cannot read rather than
// skipping it.
func promqlSelectors(expr string) ([]selector, error) {
	tokens, err := promqlTokens(expr)
	if err != nil {
		return nil, err
	}

	var found []selector
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch t.kind {
		case tokenMatchers:
			return nil, fmt.Errorf("a selector without a metric name in %q", expr)
		case tokenIdent:
		default:
			continue
		}

		next := token{kind: tokenPunct}
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		switch {
		case promqlListModifiers[t.text]:
			if next.text == "(" {
				end, err := closingParen(tokens, i+1)
				if err != nil {
					return nil, fmt.Errorf("%w in %q", err, expr)
				}
				i = end
			}
		case promqlKeywords[t.text]:
		case next.kind == tokenPunct && next.text == "(":
			// A function or an aggregation applied to what follows.
		case next.kind == tokenIdent && (next.text == "by" || next.text == "without"):
			// An aggregation with its label list first.
		default:
			s := selector{name: t.text}
			if next.kind == tokenMatchers {
				s.matchers = next.matchers
				i++
			}
			found = append(found, s)
		}
	}
	return found, nil
}

func closingParen(tokens []token, open int) (int, error) {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch tokens[i].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unbalanced parentheses")
}

func promqlTokens(expr string) ([]token, error) {
	var tokens []token
	src := []rune(expr)
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'' || c == '`':
			value, end, err := promqlString(src, i)
			if err != nil {
				return nil, fmt.Errorf("%w in %q", err, expr)
			}
			tokens = append(tokens, token{kind: tokenString, text: value})
			i = end
		case c == '[':
			// A range or subquery: durations and Grafana's $__rate_interval.
			end := i
			for end < len(src) && src[end] != ']' {
				end++
			}
			if end == len(src) {
				return nil, fmt.Errorf("unclosed [ in %q", expr)
			}
			i = end + 1
		case c == '{':
			matchers, end, err := promqlMatchers(src, i+1)
			if err != nil {
				return nil, fmt.Errorf("%w in %q", err, expr)
			}
			tokens = append(tokens, token{kind: tokenMatchers, matchers: matchers})
			i = end
		case isIdentStart(c):
			end := i
			for end < len(src) && isIdentPart(src[end]) {
				end++
			}
			tokens = append(tokens, token{kind: tokenIdent, text: string(src[i:end])})
			i = end
		case c >= '0' && c <= '9' || c == '.':
			// Numbers and durations (5m, 1.5e3): letters belong to the number.
			end := i
			for end < len(src) && (isIdentPart(src[end]) || src[end] == '.') {
				end++
			}
			tokens = append(tokens, token{kind: tokenNumber, text: string(src[i:end])})
			i = end
		case c == '$':
			return nil, fmt.Errorf("a template variable outside a string or range in %q", expr)
		default:
			tokens = append(tokens, token{kind: tokenPunct, text: string(c)})
			i++
		}
	}

	depth := 0
	for _, t := range tokens {
		switch t.text {
		case "(":
			depth++
		case ")":
			depth--
		}
		if depth < 0 {
			break
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses in %q", expr)
	}
	return tokens, nil
}

func promqlMatchers(src []rune, i int) ([]matcher, int, error) {
	var matchers []matcher
	for {
		for i < len(src) && (src[i] == ' ' || src[i] == ',' || src[i] == '\n' || src[i] == '\t') {
			i++
		}
		if i >= len(src) {
			return nil, 0, fmt.Errorf("unclosed {")
		}
		if src[i] == '}' {
			return matchers, i + 1, nil
		}
		start := i
		for i < len(src) && isIdentPart(src[i]) {
			i++
		}
		if start == i {
			return nil, 0, fmt.Errorf("a matcher without a label name")
		}
		label := string(src[start:i])
		for i < len(src) && src[i] == ' ' {
			i++
		}
		opStart := i
		for i < len(src) && strings.ContainsRune("=!~", src[i]) {
			i++
		}
		op := string(src[opStart:i])
		if op != "=" && op != "!=" && op != "=~" && op != "!~" {
			return nil, 0, fmt.Errorf("matcher %s has operator %q", label, op)
		}
		for i < len(src) && src[i] == ' ' {
			i++
		}
		value, end, err := promqlString(src, i)
		if err != nil {
			return nil, 0, err
		}
		matchers = append(matchers, matcher{label: label, op: op, value: value})
		i = end
	}
}

// promqlString reads the quoted string at src[i] and returns its value and
// the index past its closing quote.
func promqlString(src []rune, i int) (string, int, error) {
	if i >= len(src) || !strings.ContainsRune("\"'`", src[i]) {
		return "", 0, fmt.Errorf("expected a quoted string")
	}
	quote := src[i]
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		switch {
		case src[j] == '\\' && quote != '`' && j+1 < len(src):
			j++
			b.WriteRune(src[j])
		case src[j] == quote:
			return b.String(), j + 1, nil
		default:
			b.WriteRune(src[j])
		}
	}
	return "", 0, fmt.Errorf("unclosed string")
}

func isIdentStart(c rune) bool {
	return c == '_' || c == ':' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentPart(c rune) bool {
	return isIdentStart(c) || c >= '0' && c <= '9'
}

// yamlKey matches a key at the start of a line, in a list item or not.
var yamlKey = regexp.MustCompile(`^(\s*(?:- )?)([A-Za-z_]+):\s*(.*)$`)

// yamlScalars returns the value of every key named key in a YAML file. It
// reads the forms these files use: a plain or quoted scalar on the key's line,
// or a block scalar (| or >) indented below it. Anything else on such a key is
// an error, so a form it cannot read is reported rather than skipped.
func yamlScalars(path, key string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")

	var values []string
	for i := 0; i < len(lines); i++ {
		m := yamlKey.FindStringSubmatch(lines[i])
		if m == nil || m[2] != key {
			continue
		}
		indent, rest := len(m[1]), strings.TrimSpace(m[3])
		switch {
		case strings.HasPrefix(rest, "|") || strings.HasPrefix(rest, ">"):
			var block []string
			for i+1 < len(lines) && (strings.TrimSpace(lines[i+1]) == "" || leadingSpaces(lines[i+1]) > indent) {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			values = append(values, strings.TrimSpace(strings.Join(block, "\n")))
		case strings.HasPrefix(rest, "'") && strings.HasSuffix(rest, "'") && len(rest) > 1:
			values = append(values, strings.ReplaceAll(rest[1:len(rest)-1], "''", "'"))
		case strings.HasPrefix(rest, `"`):
			return nil, fmt.Errorf("%s:%d: double-quoted %s is not read; use single quotes or a block", path, i+1, key)
		case rest == "":
			return nil, fmt.Errorf("%s:%d: %s has no value on its line", path, i+1, key)
		default:
			values = append(values, rest)
		}
	}
	return values, nil
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// grafanaDashboard is the part of a dashboard's JSON that holds queries.
type grafanaDashboard struct {
	Panels     []grafanaPanel `json:"panels"`
	Templating struct {
		List []struct {
			Name  string          `json:"name"`
			Type  string          `json:"type"`
			Query json.RawMessage `json:"query"`
		} `json:"list"`
	} `json:"templating"`
}

type grafanaPanel struct {
	Title   string         `json:"title"`
	Panels  []grafanaPanel `json:"panels"`
	Targets []struct {
		Expr string `json:"expr"`
	} `json:"targets"`
}

// labelValuesQuery is a query variable's label_values(metric, label).
var labelValuesQuery = regexp.MustCompile(`^label_values\(\s*([^,]+),\s*\w+\s*\)$`)

// dashboardQueries returns the PromQL of every panel, rows' panels included,
// with the metric of every label_values variable, and those metrics by
// variable name.
func dashboardQueries(path string) ([]string, map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var d grafanaDashboard
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	var queries []string
	var walk func([]grafanaPanel)
	walk = func(panels []grafanaPanel) {
		for _, p := range panels {
			for _, t := range p.Targets {
				queries = append(queries, t.Expr)
			}
			walk(p.Panels)
		}
	}
	walk(d.Panels)

	variables := map[string]string{}
	for _, v := range d.Templating.List {
		if v.Type != "query" {
			continue
		}
		var q struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(v.Query, &q); err != nil {
			return nil, nil, fmt.Errorf("%s: variable query: %w", path, err)
		}
		m := labelValuesQuery.FindStringSubmatch(q.Query)
		if m == nil {
			return nil, nil, fmt.Errorf("%s: variable query %q is not label_values(metric, label)", path, q.Query)
		}
		queries = append(queries, m[1])
		variables[v.Name] = strings.TrimSpace(m[1])
	}
	return queries, variables, nil
}
