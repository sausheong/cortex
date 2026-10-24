package model

import (
	"context"
	"math"
	"testing"
	"time"
)

// compile-time check: a type implementing DetectConflicts satisfies Reconciler.
type stubReconciler struct{}

func (stubReconciler) DetectConflicts(_ context.Context, _ []Memory) ([]ConflictPair, error) {
	return nil, nil
}

func TestReconcilerInterfaceSatisfied(t *testing.T) {
	var r Reconciler = stubReconciler{}
	if r == nil {
		t.Fatal("expected non-nil Reconciler")
	}
	// ConflictPair / Supersession / ReconcileReport must exist with expected fields.
	_ = ConflictPair{StaleID: "a", SupersededByID: "b", Reason: "x"}
	_ = Supersession{StaleID: "a", SupersededByID: "b"}
	_ = ReconcileReport{EntitiesScanned: 0}
	_ = RejectedPair{StaleID: "a"}
}

func TestCoerceConfidence(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"zero defaults to one", 0, 1.0},
		{"NaN defaults to one", math.NaN(), 1.0},
		{"negative clamped to zero", -0.5, 0},
		{"above one clamped to one", 1.5, 1},
		{"in-range passthrough", 0.42, 0.42},
		{"one passthrough", 1.0, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CoerceConfidence(tc.in)
			if got != tc.want {
				t.Errorf("coerceConfidence(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestProfileConfigDefaults(t *testing.T) {
	cfg := DefaultProfileConfig()
	if cfg.TTL != ProfileDefaultTTL {
		t.Errorf("ttl = %v, want %v", cfg.TTL, ProfileDefaultTTL)
	}
	if cfg.RecentK != ProfileDefaultRecentK {
		t.Errorf("recentK = %d, want %d", cfg.RecentK, ProfileDefaultRecentK)
	}
	if cfg.Window != ProfileDefaultWindow {
		t.Errorf("window = %v, want %v", cfg.Window, ProfileDefaultWindow)
	}
	if cfg.StaticCap != ProfileDefaultStaticCap {
		t.Errorf("staticCap = %d, want %d", cfg.StaticCap, ProfileDefaultStaticCap)
	}
}

func TestProfileOptionsOverride(t *testing.T) {
	cfg := DefaultProfileConfig()
	for _, o := range []ProfileOption{
		WithProfileTTL(time.Hour),
		WithProfileRecentK(3),
		WithProfileWindow(48 * time.Hour),
		WithProfileStaticCap(5),
	} {
		o(&cfg)
	}
	if cfg.TTL != time.Hour || cfg.RecentK != 3 || cfg.Window != 48*time.Hour || cfg.StaticCap != 5 {
		t.Errorf("options did not apply: %+v", cfg)
	}
}

func TestWithoutProfileSkips(t *testing.T) {
	var mc MaintainConfig
	WithoutProfile()(&mc)
	if !mc.SkipProfile {
		t.Error("WithoutProfile did not set skipProfile")
	}
}
