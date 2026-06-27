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

// QA classes. Positive items are answerable; abstain items are negatives the
// engine SHOULD signal it cannot answer. "easy" negatives are out-of-domain
// (a personal graph cannot hold them); "hard" negatives are counterfactual —
// vocabulary-close to a real memory but asking an absent/false fact.
const (
	ClassPositive    = "positive"
	ClassAbstainEasy = "abstain_easy"
	ClassAbstainHard = "abstain_hard"
)

// QA is one ground-truth item: a question and the target memory it was
// derived from. TargetContent is the exact memory content used for hit
// matching. Abstain marks a negative example — a question the graph should
// NOT be able to answer (used to score abstention). Class labels the item as
// positive / easy negative / hard negative; when empty, by-class metrics fall
// back to the Abstain bool (so older eval files still load and score).
type QA struct {
	Question      string `json:"question"`
	TargetID      string `json:"target_id,omitempty"`
	TargetContent string `json:"target_content,omitempty"`
	Source        string `json:"source,omitempty"`
	Abstain       bool   `json:"abstain,omitempty"`
	Class         string `json:"class,omitempty"`
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

// AbstentionAccuracyByClass scores the abstain decision over only the abstain
// items whose Class matches `class`: the fraction the engine correctly flagged
// abstain=true. Returns 0 when there are no items of that class.
func AbstentionAccuracyByClass(items []QA, abstained []bool, class string) float64 {
	var correct, total int
	for i, it := range items {
		if !it.Abstain || it.Class != class {
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

// SweepRow is abstention quality at one candidate threshold, computed from raw
// per-item Strengths (an item abstains at threshold t iff strength < t).
type SweepRow struct {
	Threshold      float64 `json:"threshold"`
	AbstainAccEasy float64 `json:"abstain_acc_easy"` // over easy negatives
	AbstainAccHard float64 `json:"abstain_acc_hard"` // over hard negatives
	AbstainAccAll  float64 `json:"abstain_acc_all"`  // over all negatives
	FalseAbstain   float64 `json:"false_abstain"`    // positives wrongly abstained (lower better)
}

// ThresholdSweep computes a SweepRow per candidate threshold. strengths[i] is
// the cosine relevance recorded for item i (index-aligned to items). Items whose
// strength is unavailable should be passed as a negative/sentinel by the caller
// only if intentionally excluded; normally pass the recorded Strength for every
// item. The decision rule mirrors Task 1: an item abstains at threshold t iff
// strength < t.
func ThresholdSweep(items []QA, strengths []float64, thresholds []float64) []SweepRow {
	rows := make([]SweepRow, 0, len(thresholds))
	for _, t := range thresholds {
		var easyN, easyAbs int
		var hardN, hardAbs int
		var allN, allAbs int
		var posN, posAbs int
		for i, it := range items {
			abstains := strengths[i] < t
			if it.Abstain {
				allN++
				if abstains {
					allAbs++
				}
				switch it.Class {
				case ClassAbstainEasy:
					easyN++
					if abstains {
						easyAbs++
					}
				case ClassAbstainHard:
					hardN++
					if abstains {
						hardAbs++
					}
				}
			} else {
				posN++
				if abstains {
					posAbs++
				}
			}
		}
		rows = append(rows, SweepRow{
			Threshold:      t,
			AbstainAccEasy: ratio(easyAbs, easyN),
			AbstainAccHard: ratio(hardAbs, hardN),
			AbstainAccAll:  ratio(allAbs, allN),
			FalseAbstain:   ratio(posAbs, posN),
		})
	}
	return rows
}

// ratio guards an empty denominator → 0.
func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// DefaultThresholds returns 0.00, 0.05, ... 0.90 — the candidate grid for the
// sweep.
func DefaultThresholds() []float64 {
	const step = 0.05
	out := make([]float64, 0, 19)
	for i := 0; i < 19; i++ {
		out = append(out, float64(i)*step)
	}
	return out
}
