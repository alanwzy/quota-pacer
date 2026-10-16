package xai

import (
	"context"
	"net/http"
	"testing"
	"time"

	"quota-pacer/internal/host"
)

// realPaidOAuthBilling 是现网付费 OAuth 账号 billing?format=credits 的脱敏样本。
const realPaidOAuthBilling = `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-10-15T23:51:53.108427+00:00","end":"2026-10-22T23:51:53.108427+00:00"},"creditUsagePercent":1.0,"onDemandCap":{"val":0},"onDemandUsed":{"val":0},"productUsage":[{"product":"GrokBuild","usagePercent":1.0}],"isUnifiedBillingUser":true,"prepaidBalance":{"val":0},"topUpMethod":"TOP_UP_METHOD_SAVED_PAYMENT_METHOD","billingPeriodStart":"2026-10-01T00:00:00+00:00","billingPeriodEnd":"2026-11-01T00:00:00+00:00"}}`

func TestParseOAuthBillingWeekly(t *testing.T) {
	weeklyEnd := time.Date(2026, 10, 22, 23, 51, 53, 108427000, time.UTC)
	tests := []struct {
		name          string
		body          string
		wantReset     *time.Time
		wantUsed      *float64
		wantRemaining int64
		wantRemOK     bool
	}{
		{
			name:          "real USAGE_PERIOD_TYPE_WEEKLY with creditUsagePercent",
			body:          realPaidOAuthBilling,
			wantReset:     &weeklyEnd,
			wantUsed:      floatPtr(1.0),
			wantRemaining: 99,
			wantRemOK:     true,
		},
		{
			name:      "legacy weekly without creditUsagePercent",
			body:      `{"config":{"currentPeriod":{"type":"weekly","end":"2026-10-22T23:51:53Z"}}}`,
			wantReset: timePtr(time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)),
		},
		{
			name:          "lowercase enum and fractional usage rounds up",
			body:          `{"config":{"currentPeriod":{"type":"usage_period_type_weekly","end":"2026-10-22T23:51:53Z"},"creditUsagePercent":42.1}}`,
			wantReset:     timePtr(time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)),
			wantUsed:      floatPtr(42.1),
			wantRemaining: 57,
			wantRemOK:     true,
		},
		{
			name:          "over 100 percent used clamps to zero",
			body:          `{"config":{"currentPeriod":{"type":"weekly","end":"2026-10-22T23:51:53Z"},"creditUsagePercent":130}}`,
			wantReset:     timePtr(time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)),
			wantUsed:      floatPtr(130),
			wantRemaining: 0,
			wantRemOK:     true,
		},
		{
			name:          "zero percent used is full",
			body:          `{"config":{"current_period":{"type":"weekly","end":"2026-10-22T23:51:53Z"},"credit_usage_percent":0}}`,
			wantReset:     timePtr(time.Date(2026, 10, 22, 23, 51, 53, 0, time.UTC)),
			wantUsed:      floatPtr(0),
			wantRemaining: 100,
			wantRemOK:     true,
		},
		{
			name:     "monthly period ignored even with usage",
			body:     `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","end":"2026-11-01T00:00:00Z"},"creditUsagePercent":5,"billingPeriodEnd":"2026-11-01T00:00:00Z"}}`,
			wantUsed: floatPtr(5),
		},
		{
			name: "plain monthly ignored",
			body: `{"config":{"currentPeriod":{"type":"monthly","end":"2026-11-01T00:00:00Z"}}}`,
		},
		{
			name: "billingPeriodEnd only is not a long window",
			body: `{"config":{"billingPeriodEnd":"2026-11-01T00:00:00Z"}}`,
		},
		{
			name: "empty body",
			body: ``,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseOAuthBillingWeekly([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseOAuthBillingWeekly() error = %v", err)
			}
			if !timePtrEqual(got.ResetAt, tc.wantReset) {
				t.Fatalf("ResetAt = %v, want %v", got.ResetAt, tc.wantReset)
			}
			if (got.UsedPercent == nil) != (tc.wantUsed == nil) || (got.UsedPercent != nil && *got.UsedPercent != *tc.wantUsed) {
				t.Fatalf("UsedPercent = %v, want %v", got.UsedPercent, tc.wantUsed)
			}
			rem, ok := got.RemainingPercent()
			if ok != tc.wantRemOK || rem != tc.wantRemaining {
				t.Fatalf("RemainingPercent() = (%d, %v), want (%d, %v)", rem, ok, tc.wantRemaining, tc.wantRemOK)
			}
			// 兼容入口与新解析器一致。
			legacy, err := ParseWeeklyLongWindowReset([]byte(tc.body))
			if err != nil || !timePtrEqual(legacy, tc.wantReset) {
				t.Fatalf("ParseWeeklyLongWindowReset() = (%v, %v), want %v", legacy, err, tc.wantReset)
			}
		})
	}
}

func TestParseOAuthBillingWeekly_MalformedJSON(t *testing.T) {
	if _, err := ParseOAuthBillingWeekly([]byte(`{"config":`)); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

type billingHostDoer struct {
	oauthBilling string
}

func (m billingHostDoer) HTTPDo(ctx context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	return m.HTTPDoRaw(ctx, req)
}

func (m billingHostDoer) HTTPDoRaw(_ context.Context, req host.HTTPRequest) (host.HTTPResponse, error) {
	if req.URL == CLIChatProxyBillingURL {
		return host.HTTPResponse{StatusCode: http.StatusOK, Body: []byte(m.oauthBilling)}, nil
	}
	return host.HTTPResponse{StatusCode: http.StatusNotFound}, nil
}

type fixedPlanClock struct{ now time.Time }

func (c fixedPlanClock) Now() time.Time { return c.now }

func TestFetchPlan_OAuthWeeklyRemainingFromCreditUsage(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		body          string
		wantWindowRem int64
		wantLongRem   *int64
	}{
		{name: "creditUsagePercent present", body: realPaidOAuthBilling, wantWindowRem: 99, wantLongRem: int64Ptr(99)},
		{name: "creditUsagePercent absent keeps 100", body: `{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-10-22T23:51:53Z"}}}`, wantWindowRem: 100},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prober := NewProber(billingHostDoer{oauthBilling: tc.body}, fixedPlanClock{now: now})
			plan := prober.FetchPlan(context.Background(), PlanRequest{AuthIndex: "a1", AuthKind: "oauth"})
			if !plan.LongWindowBillingSeen || plan.LongWindowResetAt == nil {
				t.Fatalf("weekly long window not recognized: seen=%v reset=%v", plan.LongWindowBillingSeen, plan.LongWindowResetAt)
			}
			if len(plan.Windows) != 1 || plan.Windows[0].Remaining != tc.wantWindowRem {
				t.Fatalf("Windows = %+v, want weekly remaining %d", plan.Windows, tc.wantWindowRem)
			}
			if (plan.LongWindowRemaining == nil) != (tc.wantLongRem == nil) || (plan.LongWindowRemaining != nil && *plan.LongWindowRemaining != *tc.wantLongRem) {
				t.Fatalf("LongWindowRemaining = %v, want %v", plan.LongWindowRemaining, tc.wantLongRem)
			}
		})
	}
}

func floatPtr(v float64) *float64 { return &v }

func int64Ptr(v int64) *int64 { return &v }

func timePtr(v time.Time) *time.Time { return &v }

func timePtrEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
