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
