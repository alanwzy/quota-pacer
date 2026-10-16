package runtime

import (
	"context"
	"testing"
	"time"

	"quota-pacer/internal/core"
	"quota-pacer/internal/provider/xai"
)

func TestRecordXAIPlanResult_OAuthWeeklyRemaining(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	weeklyEnd := time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)
	rem := func(v int64) *int64 { return &v }
	tests := []struct {
		name          string
		longRemaining *int64
		billingSeen   bool
		wantRemaining int64
		wantLongRem   *int64
	}{
		{name: "1 percent used", longRemaining: rem(99), billingSeen: true, wantRemaining: 99, wantLongRem: rem(99)},
		{name: "fully used", longRemaining: rem(0), billingSeen: true, wantRemaining: 0, wantLongRem: rem(0)},
		{name: "creditUsagePercent absent keeps placeholder", billingSeen: true, wantRemaining: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			plan := xai.PlanResult{
				Provider:              core.ProviderXAI,
				AuthIndex:             "xai-1",
				ObservedAt:            now,
				PlanClass:             xai.PlanClassPaid,
				PlanType:              core.PlanTypePlus,
				LongWindowResetAt:     &weeklyEnd,
				LongWindowRemaining:   tc.longRemaining,
				LongWindowBillingSeen: tc.billingSeen,
			}
			evidence, err := recordXAIPlanResult(ctx, store, plan, now)
			if err != nil {
				t.Fatalf("recordXAIPlanResult() error = %v", err)
			}
			if evidence.Remaining == nil || *evidence.Remaining != tc.wantRemaining {
				t.Fatalf("Remaining = %v, want %d", evidence.Remaining, tc.wantRemaining)
			}
			if (evidence.LongWindowRemaining == nil) != (tc.wantLongRem == nil) || (evidence.LongWindowRemaining != nil && *evidence.LongWindowRemaining != *tc.wantLongRem) {
				t.Fatalf("LongWindowRemaining = %v, want %v", evidence.LongWindowRemaining, tc.wantLongRem)
			}
			entry, ok := store.GetEntry("xai-1", "")
			if !ok {
				t.Fatal("store entry missing")
			}
			if int64(entry.Remaining) != tc.wantRemaining {
				t.Fatalf("store Remaining = %d, want %d", entry.Remaining, tc.wantRemaining)
			}
			if (entry.LongWindowRemaining == nil) != (tc.wantLongRem == nil) {
				t.Fatalf("store LongWindowRemaining = %v, want %v", entry.LongWindowRemaining, tc.wantLongRem)
			}
		})
	}
}

func TestRecordXAIPlanResult_BillingSeenWithoutWeeklyClearsLongWindowRemaining(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	weeklyEnd := time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)
	used := int64(80)
	base := xai.PlanResult{Provider: core.ProviderXAI, AuthIndex: "xai-1", ObservedAt: now, PlanClass: xai.PlanClassPaid}
	first := base
	first.LongWindowResetAt = &weeklyEnd
	first.LongWindowRemaining = &used
	first.LongWindowBillingSeen = true
	if _, err := recordXAIPlanResult(ctx, store, first, now); err != nil {
		t.Fatal(err)
	}
	second := base
	second.LongWindowBillingSeen = true
	evidence, err := recordXAIPlanResult(ctx, store, second, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if evidence.LongWindowResetAt != nil || evidence.LongWindowRemaining != nil {
		t.Fatalf("stale long window kept: reset=%v remaining=%v", evidence.LongWindowResetAt, evidence.LongWindowRemaining)
	}
	entry, _ := store.GetEntry("xai-1", "")
	if entry.LongWindowRemaining != nil {
		t.Fatalf("store LongWindowRemaining = %v, want nil", *entry.LongWindowRemaining)
	}
}

func TestApplyXAIUsageSuccess_PreservesBillingRemaining(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	weeklyEnd := time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)
	remaining := int64(99)
	plan := xai.PlanResult{Provider: core.ProviderXAI, AuthIndex: "xai-1", ObservedAt: now, PlanClass: xai.PlanClassPaid, LongWindowResetAt: &weeklyEnd, LongWindowRemaining: &remaining, LongWindowBillingSeen: true}
	if _, err := recordXAIPlanResult(ctx, store, plan, now); err != nil {
		t.Fatal(err)
	}
	ok := true
	changed, err := applyXAIUsageToStore(ctx, store, usageRecord{AuthIndex: "xai-1", Provider: "xai", Success: &ok, StatusCode: 200}, now.Add(time.Minute))
	if err != nil || !changed {
		t.Fatalf("applyXAIUsageToStore() = (%v, %v)", changed, err)
	}
	entry, _ := store.GetEntry("xai-1", "")
	if entry.Remaining != 99 {
		t.Fatalf("Remaining after usage success = %d, want 99", entry.Remaining)
	}
	if entry.LongWindowRemaining == nil || *entry.LongWindowRemaining != 99 || !entry.LongWindowResetAt.Equal(weeklyEnd) {
		t.Fatalf("long window not preserved: reset=%v remaining=%v", entry.LongWindowResetAt, entry.LongWindowRemaining)
	}
}
