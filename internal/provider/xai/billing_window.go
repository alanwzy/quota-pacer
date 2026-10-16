package xai

import (
	"encoding/json"
	"math"
	"strings"
	"time"
)

// CLIChatProxyBillingURL 是官方 xAI OAuth 周账单查询端点（Management Center 合同）。
const CLIChatProxyBillingURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"

// oauthBillingDocument 仅建模 Management Center 合同中与周长窗相关的字段。
// 支持官方 camelCase 与 snake_case 别名；禁止发明其它端点/字段。
type oauthBillingDocument struct {
	Config *oauthBillingConfig `json:"config"`
}

type oauthBillingConfig struct {
	CurrentPeriod         *oauthBillingPeriod `json:"currentPeriod"`
	CurrentPeriodSnake    *oauthBillingPeriod `json:"current_period"`
	BillingPeriodEnd      string              `json:"billingPeriodEnd"`
	BillingPeriodEndSnake string              `json:"billing_period_end"`
	// CreditUsagePercent 是当前周期已用百分比（0..100，浮点）；缺失时为 nil。
	CreditUsagePercent      *float64 `json:"creditUsagePercent"`
	CreditUsagePercentSnake *float64 `json:"credit_usage_percent"`
}

type oauthBillingPeriod struct {
	Type string `json:"type"`
	End  string `json:"end"`
}

// OAuthBillingWeekly 是 OAuth 周账单解析结果。
type OAuthBillingWeekly struct {
	// ResetAt 是周长窗重置时刻（UTC）；非 weekly 周期为 nil。
	ResetAt *time.Time
	// UsedPercent 是 creditUsagePercent（已用百分比）；字段缺失时为 nil。
	UsedPercent *float64
}

// RemainingPercent 将已用百分比换算为剩余百分比 clamp(100-ceil(used),0,100)；
// 仅当周长窗与 creditUsagePercent 同时存在时返回 ok=true。
func (w OAuthBillingWeekly) RemainingPercent() (int64, bool) {
	if w.ResetAt == nil || w.ResetAt.IsZero() || w.UsedPercent == nil {
		return 0, false
	}
	used := *w.UsedPercent
	if math.IsNaN(used) || math.IsInf(used, 0) {
		return 0, false
	}
	remaining := 100 - int64(math.Ceil(used))
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return remaining, true
}

// ParseWeeklyLongWindowReset 从 OAuth billing JSON 提取周长窗重置时刻。
// 仅当 config.currentPeriod(type=weekly).end 为合法 RFC3339 时返回 UTC 时间；
// monthly 的 billingPeriodEnd / currentPeriod.type=monthly 一律忽略（非 rate-limit 容量重置）；
// 畸形/缺失/不支持 period 返回 (nil, nil) 或 (nil, err)，不得 panic。
func ParseWeeklyLongWindowReset(body []byte) (*time.Time, error) {
	weekly, err := ParseOAuthBillingWeekly(body)
	return weekly.ResetAt, err
}

// ParseOAuthBillingWeekly 从 OAuth billing JSON 同时提取周长窗重置时刻与 creditUsagePercent。
// period type 接受 "weekly" 与现网枚举 "USAGE_PERIOD_TYPE_WEEKLY"（大小写不敏感）。
func ParseOAuthBillingWeekly(body []byte) (OAuthBillingWeekly, error) {
	var out OAuthBillingWeekly
	if len(strings.TrimSpace(string(body))) == 0 {
		return out, nil
	}
	var doc oauthBillingDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return out, err
	}
	if doc.Config == nil {
		return out, nil
	}
	out.UsedPercent = doc.Config.CreditUsagePercent
	if out.UsedPercent == nil {
		out.UsedPercent = doc.Config.CreditUsagePercentSnake
	}
	period := doc.Config.CurrentPeriod
	if period == nil {
		period = doc.Config.CurrentPeriodSnake
	}
	if period == nil {
		// billingPeriodEnd 仅为账单周期元数据，不得映射为 LongWindowResetAt。
		return out, nil
	}
	if normalizeBillingPeriodType(period.Type) != "weekly" {
		// monthly / 空 / 未知类型：明确不作为长窗
		return out, nil
	}
	endRaw := strings.TrimSpace(period.End)
	if endRaw == "" {
		return out, nil
	}
	end, err := parseRFC3339UTC(endRaw)
	if err != nil {
		return out, nil
	}
	out.ResetAt = &end
	return out, nil
}

// normalizeBillingPeriodType 统一 period type：小写并去掉 "usage_period_type_" 枚举前缀。
func normalizeBillingPeriodType(raw string) string {
	t := strings.ToLower(strings.TrimSpace(raw))
	return strings.TrimPrefix(t, "usage_period_type_")
}

func parseRFC3339UTC(raw string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// isOAuthAuthKind 判断 auth JSON 的 auth_kind 是否为 OAuth（官方小写 oauth）。
func isOAuthAuthKind(authKind string) bool {
	return strings.EqualFold(strings.TrimSpace(authKind), "oauth")
}
