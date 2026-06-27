package eval

import (
	"math"
	"testing"
)

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestHitRank(t *testing.T) {
	results := []string{
		"Bob likes tea",
		"Alice joined Stripe in March",
		"The sky is blue",
	}
	// Exact-ish contains match at rank 2.
	if r := hitRank("Alice joined Stripe in March", results); r != 2 {
		t.Fatalf("expected rank 2, got %d", r)
	}
	// Normalization: case + whitespace differences still match.
	if r := hitRank("alice   joined stripe IN march", results); r != 2 {
		t.Fatalf("normalized match expected rank 2, got %d", r)
	}
	// Miss.
	if r := hitRank("Carol moved to Berlin", results); r != 0 {
		t.Fatalf("expected miss (0), got %d", r)
	}
	// Empty target never matches.
	if r := hitRank("", results); r != 0 {
		t.Fatalf("empty target should be 0, got %d", r)
	}
}

func TestRecallAtK(t *testing.T) {
	items := []QA{
		{Question: "q1", TargetContent: "a"},          // rank 1
		{Question: "q2", TargetContent: "b"},          // rank 5
		{Question: "q3", TargetContent: "c"},          // miss
		{Question: "q4", Abstain: true},               // excluded
	}
	ranks := []int{1, 5, 0, 0}

	// @3: only item1 hits (rank 1<=3); item2 rank 5>3; item3 miss. 1/3.
	if got := RecallAtK(items, ranks, 3); !approxEq(got, 1.0/3.0) {
		t.Fatalf("recall@3: got %v want %v", got, 1.0/3.0)
	}
	// @5: item1 + item2 hit. 2/3.
	if got := RecallAtK(items, ranks, 5); !approxEq(got, 2.0/3.0) {
		t.Fatalf("recall@5: got %v want %v", got, 2.0/3.0)
	}
	// Abstain item excluded from denominator (3 positives, not 4).
}

func TestMRR(t *testing.T) {
	items := []QA{
		{TargetContent: "a"}, // rank 1 → 1.0
		{TargetContent: "b"}, // rank 4 → 0.25
		{TargetContent: "c"}, // miss → 0
		{Abstain: true},      // excluded
	}
	ranks := []int{1, 4, 0, 0}
	want := (1.0 + 0.25 + 0.0) / 3.0
	if got := MRR(items, ranks); !approxEq(got, want) {
		t.Fatalf("MRR: got %v want %v", got, want)
	}
}

func TestAbstentionAccuracyAndFalseRate(t *testing.T) {
	items := []QA{
		{TargetContent: "a"},            // positive
		{TargetContent: "b"},            // positive
		{Abstain: true},                 // negative
		{Abstain: true},                 // negative
		{Abstain: true},                 // negative
	}
	// Engine said abstain for: positive#2 (wrong), negative#1 (right),
	// negative#3 (right). negative#2 it did NOT abstain (wrong).
	abstained := []bool{false, true, true, false, true}

	// Abstention accuracy over the 3 negatives: 2 correct → 2/3.
	if got := AbstentionAccuracy(items, abstained); !approxEq(got, 2.0/3.0) {
		t.Fatalf("abstention accuracy: got %v want %v", got, 2.0/3.0)
	}
	// False abstention over the 2 positives: 1 wrongly abstained → 1/2.
	if got := FalseAbstentionRate(items, abstained); !approxEq(got, 0.5) {
		t.Fatalf("false abstention rate: got %v want %v", got, 0.5)
	}
}

func TestAbstentionAccuracyByClass(t *testing.T) {
	items := []QA{
		{TargetContent: "a", Class: ClassPositive},                 // positive
		{TargetContent: "b", Class: ClassPositive},                 // positive
		{Abstain: true, Class: ClassAbstainEasy},                   // easy neg #1
		{Abstain: true, Class: ClassAbstainEasy},                   // easy neg #2
		{Abstain: true, Class: ClassAbstainEasy},                   // easy neg #3
		{Abstain: true, Class: ClassAbstainHard},                   // hard neg #1
		{Abstain: true, Class: ClassAbstainHard},                   // hard neg #2
	}
	// index:        0      1     2     3      4     5      6
	abstained := []bool{false, true, true, false, true, true, false}
	// Easy negatives (idx 2,3,4): engine abstained on 2,4 not 3 → 2/3.
	if got := AbstentionAccuracyByClass(items, abstained, ClassAbstainEasy); !approxEq(got, 2.0/3.0) {
		t.Fatalf("easy by-class: got %v want %v", got, 2.0/3.0)
	}
	// Hard negatives (idx 5,6): engine abstained on 5 not 6 → 1/2.
	if got := AbstentionAccuracyByClass(items, abstained, ClassAbstainHard); !approxEq(got, 0.5) {
		t.Fatalf("hard by-class: got %v want %v", got, 0.5)
	}
	// Positive class has no abstain items → 0.
	if got := AbstentionAccuracyByClass(items, abstained, ClassPositive); got != 0 {
		t.Fatalf("positive by-class should be 0, got %v", got)
	}
	// Absent class → 0.
	if got := AbstentionAccuracyByClass(items, abstained, "no_such_class"); got != 0 {
		t.Fatalf("absent class should be 0, got %v", got)
	}
}

func TestThresholdSweep(t *testing.T) {
	// Two positives (high strength), one easy neg (very low), one hard neg (mid).
	items := []QA{
		{Class: ClassPositive},                   // positive, strength 0.80
		{Class: ClassPositive},                   // positive, strength 0.60
		{Abstain: true, Class: ClassAbstainEasy}, // easy neg, strength 0.05
		{Abstain: true, Class: ClassAbstainHard}, // hard neg, strength 0.40
	}
	strengths := []float64{0.80, 0.60, 0.05, 0.40}

	rows := ThresholdSweep(items, strengths, []float64{0.10, 0.50})
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// At t=0.10: abstain iff strength<0.10.
	//   easy (0.05<0.10) → 1/1 = 1.0
	//   hard (0.40<0.10) → 0/1 = 0.0
	//   all  (easy yes, hard no) → 1/2 = 0.5
	//   positives (0.80,0.60 both >=0.10) → 0/2 false-abstain = 0.0
	low := rows[0]
	if low.Threshold != 0.10 {
		t.Fatalf("row0 threshold: got %v want 0.10", low.Threshold)
	}
	if !approxEq(low.AbstainAccEasy, 1.0) {
		t.Fatalf("t=0.10 easy: got %v want 1.0", low.AbstainAccEasy)
	}
	if !approxEq(low.AbstainAccHard, 0.0) {
		t.Fatalf("t=0.10 hard: got %v want 0.0", low.AbstainAccHard)
	}
	if !approxEq(low.AbstainAccAll, 0.5) {
		t.Fatalf("t=0.10 all: got %v want 0.5", low.AbstainAccAll)
	}
	if !approxEq(low.FalseAbstain, 0.0) {
		t.Fatalf("t=0.10 false-abstain: got %v want 0.0", low.FalseAbstain)
	}

	// At t=0.50: abstain iff strength<0.50.
	//   easy (0.05<0.50) → 1.0
	//   hard (0.40<0.50) → 1.0
	//   all  → 2/2 = 1.0
	//   positives (0.60>=0.50 no, 0.80 no) → 0/2 = 0.0 false-abstain
	high := rows[1]
	if !approxEq(high.AbstainAccAll, 1.0) {
		t.Fatalf("t=0.50 all: got %v want 1.0", high.AbstainAccAll)
	}
	// Monotone trade captured: all-abstain accuracy rose (0.5 → 1.0).
	if !(high.AbstainAccAll > low.AbstainAccAll) {
		t.Fatalf("expected AbstainAccAll to rise with threshold: %v -> %v", low.AbstainAccAll, high.AbstainAccAll)
	}
	if !approxEq(high.FalseAbstain, 0.0) {
		t.Fatalf("t=0.50 false-abstain: got %v want 0.0", high.FalseAbstain)
	}
}

func TestThresholdSweepFalseAbstainRises(t *testing.T) {
	// A positive with a low-ish strength gets wrongly abstained at a high threshold.
	items := []QA{
		{Class: ClassPositive},                   // positive, strength 0.30
		{Abstain: true, Class: ClassAbstainHard}, // hard neg, strength 0.20
	}
	strengths := []float64{0.30, 0.20}
	rows := ThresholdSweep(items, strengths, []float64{0.10, 0.50})
	// t=0.10: positive 0.30>=0.10 → false-abstain 0; hard 0.20>=0.10 → acc 0.
	if !approxEq(rows[0].FalseAbstain, 0.0) {
		t.Fatalf("t=0.10 false-abstain: got %v want 0.0", rows[0].FalseAbstain)
	}
	// t=0.50: positive 0.30<0.50 → false-abstain 1/1; hard 0.20<0.50 → acc 1/1.
	if !approxEq(rows[1].FalseAbstain, 1.0) {
		t.Fatalf("t=0.50 false-abstain: got %v want 1.0", rows[1].FalseAbstain)
	}
	if !(rows[1].FalseAbstain > rows[0].FalseAbstain) {
		t.Fatalf("expected FalseAbstain to rise with threshold: %v -> %v", rows[0].FalseAbstain, rows[1].FalseAbstain)
	}
}

func TestDefaultThresholds(t *testing.T) {
	ts := DefaultThresholds()
	if len(ts) != 19 {
		t.Fatalf("expected 19 thresholds, got %d", len(ts))
	}
	if !approxEq(ts[0], 0.00) {
		t.Fatalf("first threshold: got %v want 0.00", ts[0])
	}
	if !approxEq(ts[len(ts)-1], 0.90) {
		t.Fatalf("last threshold: got %v want 0.90", ts[len(ts)-1])
	}
	for i := 1; i < len(ts); i++ {
		if !approxEq(ts[i]-ts[i-1], 0.05) {
			t.Fatalf("step at %d: got %v want 0.05", i, ts[i]-ts[i-1])
		}
	}
}

func TestEmptySetsAreZero(t *testing.T) {
	if RecallAtK(nil, nil, 5) != 0 {
		t.Fatal("recall@k of empty should be 0")
	}
	if MRR(nil, nil) != 0 {
		t.Fatal("MRR of empty should be 0")
	}
	if AbstentionAccuracy(nil, nil) != 0 {
		t.Fatal("abstention accuracy of empty should be 0")
	}
	// All-positive set → abstention accuracy 0 (no negatives), not a panic.
	items := []QA{{TargetContent: "a"}}
	if AbstentionAccuracy(items, []bool{false}) != 0 {
		t.Fatal("abstention accuracy with no negatives should be 0")
	}
}
