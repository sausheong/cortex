// Package eval is a retrieval-quality benchmark harness for cortex. It scores
// how well Recall surfaces a known target memory for a generated question,
// across different retrieval configurations (ablation), so the recall-quality
// work can be expressed as numbers against a real graph rather than asserted.
//
// Ground truth is synthetic: an LLM writes a natural question from each
// sampled memory, and a "hit" means that memory's content is returned within
// the top-k recall results. Abstention questions (about facts NOT in the
// graph) score on whether Recall correctly signals it has nothing.
package eval

import "strings"

// QA is one ground-truth item: a question and the target memory it was
// derived from. TargetContent is the exact memory content used for hit
// matching. Abstain marks a negative example — a question the graph should
// NOT be able to answer (used to score abstention).
type QA struct {
	Question      string `json:"question"`
	TargetID      string `json:"target_id,omitempty"`
	TargetContent string `json:"target_content,omitempty"`
	Source        string `json:"source,omitempty"`
	Abstain       bool   `json:"abstain,omitempty"`
}

// hitRank returns the 1-based rank of the target within results, or 0 if the
// target is not present. A result matches the target when the target content
// is contained in (or equal to) the result content, compared on normalized
// whitespace/case — robust to minor rendering differences while still
// requiring the actual fact to be present.
func hitRank(targetContent string, resultContents []string) int {
	want := normalize(targetContent)
	if want == "" {
		return 0
	}
	for i, rc := range resultContents {
		got := normalize(rc)
		if got == want || strings.Contains(got, want) || strings.Contains(want, got) {
			return i + 1
		}
	}
	return 0
}

// normalize lowercases and collapses runs of whitespace to single spaces so
// content comparison is insensitive to formatting noise.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// RecallAtK returns the fraction of positive (non-abstain) items whose target
// appears within the top k results. ranks[i] is the 1-based hit rank for
// item i (0 = miss). Items with Abstain=true are excluded from the
// denominator (they have no target to find). Returns 0 when there are no
// positive items.
func RecallAtK(items []QA, ranks []int, k int) float64 {
	var hits, total int
	for i, it := range items {
		if it.Abstain {
			continue
		}
		total++
		r := ranks[i]
		if r > 0 && r <= k {
			hits++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(hits) / float64(total)
}

// MRR is the mean reciprocal rank over positive items: average of 1/rank
// (0 for misses). Rewards ranking the target higher, not just retrieving it.
func MRR(items []QA, ranks []int) float64 {
	var sum float64
	var total int
	for i, it := range items {
		if it.Abstain {
			continue
		}
		total++
		if r := ranks[i]; r > 0 {
			sum += 1.0 / float64(r)
		}
	}
	if total == 0 {
		return 0
	}
	return sum / float64(total)
}

// AbstentionAccuracy scores the abstain decision over the abstain (negative)
// items only: the fraction where the engine correctly reported abstain=true.
// abstained[i] is what RecallWithStrength returned for item i. Returns 0 when
// there are no abstain items.
func AbstentionAccuracy(items []QA, abstained []bool) float64 {
	var correct, total int
	for i, it := range items {
		if !it.Abstain {
			continue
		}
		total++
		if abstained[i] {
			correct++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(correct) / float64(total)
}

// FalseAbstentionRate is the fraction of POSITIVE items where the engine
// wrongly abstained (said it had nothing when there was a real target). A
// good engine keeps this low while keeping AbstentionAccuracy high.
func FalseAbstentionRate(items []QA, abstained []bool) float64 {
	var wrong, total int
	for i, it := range items {
		if it.Abstain {
			continue
		}
		total++
		if abstained[i] {
			wrong++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(wrong) / float64(total)
}
