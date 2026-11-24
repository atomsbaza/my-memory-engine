package tok

import (
	_ "embed"
	"regexp"
	"strings"
	"sync"
)

//go:embed dict.txt
var rawDict string

type trieNode struct {
	next map[rune]int
	word bool
}

var (
	dictOnce sync.Once
	trie     []trieNode
)

func loadDict() {
	dictOnce.Do(func() {
		nodes := make([]trieNode, 1, 80000)
		for _, line := range strings.Split(rawDict, "\n") {
			w := strings.TrimSpace(line)
			if w == "" {
				continue
			}
			cur := 0
			for _, r := range w {
				if nodes[cur].next == nil {
					nodes[cur].next = make(map[rune]int, 2)
				}
				nxt, ok := nodes[cur].next[r]
				if !ok {
					nxt = len(nodes)
					nodes = append(nodes, trieNode{})
					nodes[cur].next[r] = nxt
				}
				cur = nxt
			}
			nodes[cur].word = true
		}
		trie = nodes
	})
}

var (
	tokenRe   = regexp.MustCompile(`[0-9A-Za-z_ก-๙]+`)
	thaiRunRe = regexp.MustCompile(`[ก-๙]+`)
)

func isThaiRune(r rune) bool {
	return r >= 'ก' && r <= '๙'
}

func hasThai(s string) bool {
	for _, r := range s {
		if isThaiRune(r) {
			return true
		}
	}
	return false
}

// Tokenize splits s into lowercased [0-9A-Za-z_ก-๙]+ runs and segments
// Thai-script runs using DP maximal matching against the embedded dictionary.
func Tokenize(s string) []string {
	matches := tokenRe.FindAllString(strings.ToLower(s), -1)
	if len(matches) == 0 {
		return nil
	}
	var out []string
	for _, m := range matches {
		if !hasThai(m) {
			out = append(out, m)
			continue
		}
		out = append(out, splitMixedToken(m)...)
	}
	return out
}

// SegmentForFTS inserts spaces between segmented Thai words in s while
// preserving all non-Thai text, casing, punctuation, and markdown syntax.
func SegmentForFTS(s string) string {
	if s == "" || !hasThai(s) {
		return s
	}
	return thaiRunRe.ReplaceAllStringFunc(s, func(run string) string {
		segs := segmentThai([]rune(run))
		return strings.Join(segs, " ")
	})
}

func splitMixedToken(tok string) []string {
	var out []string
	runes := []rune(tok)
	i := 0
	for i < len(runes) {
		j := i + 1
		thai := isThaiRune(runes[i])
		for j < len(runes) && isThaiRune(runes[j]) == thai {
			j++
		}
		if thai {
			out = append(out, segmentThai(runes[i:j])...)
		} else {
			out = append(out, string(runes[i:j]))
		}
		i = j
	}
	return out
}

// segmentThai segments a contiguous Thai-rune slice using backward DP to
// minimize total segment count (breaking ties in favor of longer leading words).
// Consecutive unknown runes are grouped into a single token so they are not
// dropped by >1-rune query filters.
func segmentThai(runes []rune) []string {
	n := len(runes)
	if n == 0 {
		return nil
	}
	loadDict()

	const inf = int(^uint(0) >> 1)
	dp := make([]int, n+1)
	nextIdx := make([]int, n)
	known := make([]bool, n)

	for i := n - 1; i >= 0; i-- {
		dp[i] = inf
		cur := 0
		// Walk the trie forward from i; record the longest-first tie-break
		// by keeping the best (lowest) dp[j+1] and preferring larger j on ties.
		var matches []int
		for j := i; j < n; j++ {
			nxt, ok := trie[cur].next[runes[j]]
			if !ok {
				break
			}
			cur = nxt
			if trie[cur].word {
				matches = append(matches, j+1)
			}
		}
		for k := len(matches) - 1; k >= 0; k-- {
			end := matches[k]
			cost := 1 + dp[end]
			if cost < dp[i] {
				dp[i] = cost
				nextIdx[i] = end
				known[i] = true
			}
		}
		// Fallback: advance 1 rune as unknown if no known path or strictly cheaper.
		if !known[i] || 1+dp[i+1] < dp[i] {
			dp[i] = 1 + dp[i+1]
			nextIdx[i] = i + 1
			known[i] = false
		}
	}

	out := make([]string, 0, n/3+1)
	unkStart := -1
	for i := 0; i < n; {
		if !known[i] {
			if unkStart == -1 {
				unkStart = i
			}
			i = nextIdx[i]
			continue
		}
		if unkStart != -1 {
			out = append(out, string(runes[unkStart:i]))
			unkStart = -1
		}
		out = append(out, string(runes[i:nextIdx[i]]))
		i = nextIdx[i]
	}
	if unkStart != -1 {
		out = append(out, string(runes[unkStart:n]))
	}
	return out
}
