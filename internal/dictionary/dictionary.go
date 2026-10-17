// Package dictionary replaces whole matches in transcripts.
//
// It mirrors the reference implementation's semantics:
//   - matching ignores case,
//   - the longest match wins at each position,
//   - a later pair overrides an earlier one that differs only by case,
//   - only whole matches are replaced (no word characters around them).
//
// Go's regexp (RE2) has no lookaround, so the Python-level
// (?<!\w)(?:...)(?!\w) boundaries are verified by hand.
package dictionary

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Pair is one match→replace entry; it is also the stored JSON shape.
type Pair struct {
	Match   string `json:"match"`
	Replace string `json:"replace"`
}

// Normalize returns the pairs as stored: whitespace-collapsed, both sides
// non-empty, and a later pair that equals an earlier one ignoring case
// replaces it.
func Normalize(pairs []Pair) []Pair {
	out := make([]Pair, 0, len(pairs))
	for _, pair := range pairs {
		match, replace := clean(pair.Match), clean(pair.Replace)
		if match == "" || replace == "" {
			continue
		}
		replaced := false
		for i, existing := range out {
			if strings.EqualFold(existing.Match, match) {
				out[i] = Pair{Match: match, Replace: replace}
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, Pair{match, replace})
		}
	}
	return out
}

// Same reports whether two entries differ only by case.
func Same(left, right string) bool { return strings.EqualFold(left, right) }

// Dump returns the normalized pairs (the JSON value stored in config).
func Dump(pairs []Pair) []Pair { return Normalize(pairs) }

// Apply replaces whole matches in text.
func Apply(text string, pairs []Pair) string {
	normalized := Normalize(pairs)
	if text == "" || len(normalized) == 0 {
		return text
	}

	// Longest match first, so the alternation prefers it at a position —
	// mirroring the reference's sorted pattern.
	ordered := append([]Pair(nil), normalized...)
	sort.Slice(ordered, func(i, j int) bool { return len(ordered[i].Match) > len(ordered[j].Match) })

	alternatives := make([]string, len(ordered))
	for i, pair := range ordered {
		alternatives[i] = regexp.QuoteMeta(pair.Match)
	}

	var b strings.Builder
	last := 0
	for _, loc := range findAll(text, strings.Join(alternatives, "|")) {
		start, end := loc[0], loc[1]
		if wordBefore(text, start) || wordAfter(text, end) {
			continue
		}
		b.WriteString(text[last:start])
		b.WriteString(choose(text[start:end], ordered))
		last = end
	}
	b.WriteString(text[last:])
	return b.String()
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// findAll reports byte ranges where any alternative matches case-insensitively.
// The alternation is ordered longest-first, so the longest candidate wins
// wherever a shorter one would also match (the reference sorts the same way).
func findAll(text, alternation string) [][]int {
	re := regexp.MustCompile(`(?i)(?:` + alternation + `)`)
	return re.FindAllStringIndex(text, -1)
}

func choose(chunk string, ordered []Pair) string {
	for _, pair := range ordered {
		// re.fullmatch(re.escape(match), chunk, ignorecase) reduces to
		// case-insensitive equality for a literal pattern.
		if strings.EqualFold(pair.Match, chunk) {
			return pair.Replace
		}
	}
	return chunk
}

func wordBefore(text string, start int) bool {
	if start <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	return isWord(r)
}

func wordAfter(text string, end int) bool {
	if end >= len(text) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(text[end:])
	return isWord(r)
}

func isWord(r rune) bool {
	switch r {
	case '_':
		return true
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}
