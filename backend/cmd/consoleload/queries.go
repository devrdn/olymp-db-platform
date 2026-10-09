package main

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// The query mix is written against the detective game (persons, calls,
// statements, cases); another game needs other queries. Each takes a random
// parameter so no query is answered from a cache warmed by another's.

type Kind string

const (
	// KindCheap is a lookup by key: an index probe and one row.
	KindCheap Kind = "cheap"
	// KindMid is a join across two tables, driven by an index on one of them.
	KindMid Kind = "mid"
	// KindExpensive is an aggregate over the largest table, read in full.
	KindExpensive Kind = "expensive"
)

var kinds = []Kind{KindCheap, KindMid, KindExpensive}

var templates = map[Kind][]func(r *rand.Rand) string{
	KindCheap: {
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT id, given_name, surname, born, phone, role FROM persons WHERE id = %d`, 1+r.IntN(200000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT id, code, title, status, opened_on FROM cases WHERE id = %d`, 1+r.IntN(480))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT id, case_id, kind, found_at, description FROM evidence WHERE id = %d`, 1+r.IntN(150000))
		},
	},
	KindMid: {
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT c.started_at, c.duration_s, p.given_name, p.surname
FROM calls c JOIN persons p ON p.id = c.callee_id
WHERE c.caller_id = %d ORDER BY c.started_at`, 1+r.IntN(200000))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT s.taken_at, p.given_name, p.surname, left(s.body, 80) AS excerpt
FROM statements s JOIN persons p ON p.id = s.person_id
WHERE s.case_id = %d ORDER BY s.taken_at`, 1+r.IntN(480))
		},
	},
	KindExpensive: {
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT tower_id, count(*) AS calls, avg(duration_s) AS average
FROM calls WHERE duration_s > %d GROUP BY tower_id ORDER BY calls DESC`, r.IntN(60))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT caller_id, count(*) AS calls, sum(duration_s) AS seconds
FROM calls WHERE started_at >= '%d-%02d-01' GROUP BY caller_id ORDER BY calls DESC LIMIT 20`,
				2023+r.IntN(3), 1+r.IntN(12))
		},
		func(r *rand.Rand) string {
			return fmt.Sprintf(`SELECT p.role, count(*) AS calls
FROM calls c JOIN persons p ON p.id = c.caller_id
WHERE c.duration_s > %d GROUP BY p.role`, r.IntN(300))
		},
	},
}

type mix map[Kind]int

func parseMix(s string) (mix, error) {
	parts := strings.Split(s, ",")
	if len(parts) != len(kinds) {
		return nil, fmt.Errorf("want %d percentages (cheap, mid, expensive), got %q", len(kinds), s)
	}
	out := mix{}
	total := 0
	for i, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 {
			return nil, fmt.Errorf("%q is not a percentage", part)
		}
		out[kinds[i]] = n
		total += n
	}
	if total != 100 {
		return nil, fmt.Errorf("the percentages add up to %d, not 100", total)
	}
	return out, nil
}

func (m mix) pick(r *rand.Rand) (Kind, string) {
	roll := r.IntN(100)
	for _, kind := range kinds {
		if roll < m[kind] {
			options := templates[kind]
			return kind, options[r.IntN(len(options))](r)
		}
		roll -= m[kind]
	}
	// Unreachable: parseMix requires shares summing to 100.
	return KindCheap, templates[KindCheap][0](r)
}
