// Package suggest finds the nearest candidate to a misspelled term
// using a bounded Levenshtein distance. The intent is to make
// "X not found" errors actionable by pointing at the most likely
// thing the user meant.
package suggest

import "strings"

// Closest returns the candidate string with the smallest Levenshtein
// distance to want, or an empty string if no candidate is within
// maxDist. Comparison is case-insensitive. When two candidates tie,
// the earlier one in the slice wins.
func Closest(want string, candidates []string, maxDist int) string {
	if want == "" || len(candidates) == 0 || maxDist < 1 {
		return ""
	}
	wantLower := strings.ToLower(want)
	best := ""
	bestDist := maxDist + 1
	for _, c := range candidates {
		d := distance(wantLower, strings.ToLower(c))
		if d < bestDist {
			bestDist = d
			best = c
		}
	}
	if bestDist > maxDist {
		return ""
	}
	return best
}

// distance is the Levenshtein edit distance between a and b.
// Iterative two-row implementation: O(len(a) * len(b)) time,
// O(min(len(a), len(b))) space.
func distance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len([]rune(b))
	}
	if len(b) == 0 {
		return len([]rune(a))
	}

	ar := []rune(a)
	br := []rune(b)
	if len(ar) < len(br) {
		ar, br = br, ar
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
