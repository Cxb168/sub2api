//go:build unit

package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

const testDingTalkWebhook = "https://oapi.dingtalk.com/robot/send?access_token=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestValidateDingTalkWebhookURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"official", testDingTalkWebhook, false},
		{"empty", "", true},
		{"http rejected", "http://oapi.dingtalk.com/robot/send?access_token=x", true},
		{"other host rejected", "https://example.com/robot/send?access_token=x", true},
		{"wrong path rejected", "https://oapi.dingtalk.com/robot/send2?access_token=x", true},
		{"missing token rejected", "https://oapi.dingtalk.com/robot/send", true},
		{"non-url rejected", "not-a-url", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateDingTalkWebhookURL(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestBuildSignedWebhookURL(t *testing.T) {
	secret := "SECtestsecret"
	now := time.UnixMilli(1767225600123)

	signed, err := BuildSignedWebhookURL(testDingTalkWebhook, secret, now)
	require.NoError(t, err)

	parsed, err := url.Parse(signed)
	require.NoError(t, err)
	require.Equal(t, "1767225600123", parsed.Query().Get("timestamp"))
	require.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", parsed.Query().Get("access_token"))

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("1767225600123" + "\n" + secret))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	require.Equal(t, expected, parsed.Query().Get("sign"))

	// 未配置 secret 时不加签，地址保持不变。
	plain, err := BuildSignedWebhookURL(testDingTalkWebhook, "", now)
	require.NoError(t, err)
	require.Equal(t, testDingTalkWebhook, plain)
	require.Empty(t, urlMustParse(t, plain).Query().Get("sign"))
}

func urlMustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed
}

func TestMaskDingTalkWebhookURL(t *testing.T) {
	masked := MaskDingTalkWebhookURL(testDingTalkWebhook)
	require.Contains(t, masked, "oapi.dingtalk.com")
	require.Contains(t, masked, "access_token=***")
	require.NotContains(t, masked, "0123456789abcdef0123456789abcdef")
	require.Equal(t, "", MaskDingTalkWebhookURL("   "))
}

func TestBuildDingTalkMarkdownPayload(t *testing.T) {
	cfg := &OpsDingTalkNotificationConfig{
		AtMobiles: []string{"13800000000"},
		AtAll:     false,
	}
	raw, err := BuildDingTalkMarkdownPayload(cfg, "标题", "### 正文")
	require.NoError(t, err)

	var decoded struct {
		MsgType  string `json:"msgtype"`
		Markdown struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"markdown"`
		At struct {
			AtMobiles []string `json:"atMobiles"`
			IsAtAll   bool     `json:"isAtAll"`
		} `json:"at"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, "markdown", decoded.MsgType)
	require.Equal(t, "标题", decoded.Markdown.Title)
	require.Equal(t, "### 正文", decoded.Markdown.Text)
	require.Equal(t, []string{"13800000000"}, decoded.At.AtMobiles)
	require.False(t, decoded.At.IsAtAll)
}

func TestParseDingTalkSendResponse(t *testing.T) {
	require.NoError(t, ParseDingTalkSendResponse(200, []byte(`{"errcode":0,"errmsg":"ok"}`)))
	require.Error(t, ParseDingTalkSendResponse(200, []byte(`{"errcode":310000,"errmsg":"sign not match"}`)))
	require.Error(t, ParseDingTalkSendResponse(500, []byte(`{"errcode":0,"errmsg":"ok"}`)))
	require.Error(t, ParseDingTalkSendResponse(200, []byte(`not json`)))
}

// =========================
// 配置读写（部分更新语义）
// =========================

func TestUpdateDingTalkNotificationConfigKeepsExistingCredentials(t *testing.T) {
	repo := newDingTalkMemorySettingRepo()
	svc := &OpsService{settingRepo: repo}
	ctx := context.Background()

	enabled := true
	webhook := testDingTalkWebhook
	secret := "SECfirst"
	rateLimit := 30
	view, err := svc.UpdateDingTalkNotificationConfig(ctx, &OpsDingTalkNotificationConfigUpdateRequest{
		Enabled:          &enabled,
		WebhookURL:       &webhook,
		Secret:           &secret,
		RateLimitPerHour: &rateLimit,
		AtMobiles:        []string{"13800000000"},
	})
	require.NoError(t, err)
	require.True(t, view.Enabled)
	require.True(t, view.WebhookConfigured)
	require.True(t, view.SecretConfigured)
	require.NotContains(t, view.WebhookURL, "0123456789abcdef0123456789abcdef")

	// 第二次只改频率：webhook/secret 传空串，必须沿用旧值。
	empty := ""
	rateLimit = 5
	view2, err := svc.UpdateDingTalkNotificationConfig(ctx, &OpsDingTalkNotificationConfigUpdateRequest{
		WebhookURL:       &empty,
		Secret:           &empty,
		RateLimitPerHour: &rateLimit,
	})
	require.NoError(t, err)
	require.Equal(t, 5, view2.RateLimitPerHour)
	require.True(t, view2.WebhookConfigured)
	require.True(t, view2.SecretConfigured)

	stored, err := svc.GetDingTalkNotificationConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, testDingTalkWebhook, stored.WebhookURL)
	require.Equal(t, "SECfirst", stored.Secret)
}

func TestUpdateDingTalkNotificationConfigRejectsBadWebhook(t *testing.T) {
	repo := newDingTalkMemorySettingRepo()
	svc := &OpsService{settingRepo: repo}

	bad := "https://example.com/robot/send?access_token=x"
	_, err := svc.UpdateDingTalkNotificationConfig(context.Background(), &OpsDingTalkNotificationConfigUpdateRequest{WebhookURL: &bad})
	require.Error(t, err)
	require.Contains(t, err.Error(), "oapi.dingtalk.com")
}

// 前端读取到的是脱敏值，原样回传时不能被当成新地址写库。
func TestUpdateDingTalkNotificationConfigIgnoresMaskedWebhookEcho(t *testing.T) {
	repo := newDingTalkMemorySettingRepo()
	svc := &OpsService{settingRepo: repo}
	ctx := context.Background()

	webhook := testDingTalkWebhook
	_, err := svc.UpdateDingTalkNotificationConfig(ctx, &OpsDingTalkNotificationConfigUpdateRequest{WebhookURL: &webhook})
	require.NoError(t, err)

	maskedEcho := MaskDingTalkWebhookURL(testDingTalkWebhook)
	_, err = svc.UpdateDingTalkNotificationConfig(ctx, &OpsDingTalkNotificationConfigUpdateRequest{WebhookURL: &maskedEcho})
	require.NoError(t, err)

	stored, err := svc.GetDingTalkNotificationConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, testDingTalkWebhook, stored.WebhookURL)
}

func TestGetDingTalkNotificationConfigViewIsRedacted(t *testing.T) {
	repo := newDingTalkMemorySettingRepo()
	svc := &OpsService{settingRepo: repo}

	view, err := svc.GetDingTalkNotificationConfigView(context.Background())
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.False(t, view.WebhookConfigured)
	require.False(t, view.SecretConfigured)
	require.Empty(t, view.WebhookURL)
	// 默认开启恢复通知（本需求要求）。
	require.True(t, view.IncludeResolvedAlerts)
}

type dingTalkMemorySettingRepo struct {
	mu     sync.RWMutex
	values map[string]string
}

func newDingTalkMemorySettingRepo() *dingTalkMemorySettingRepo {
	return &dingTalkMemorySettingRepo{values: map[string]string{}}
}

func (r *dingTalkMemorySettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *dingTalkMemorySettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *dingTalkMemorySettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = value
	return nil
}

func (r *dingTalkMemorySettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			out[key] = value
		}
	}
	return out, nil
}

func (r *dingTalkMemorySettingRepo) SetMultiple(_ context.Context, settings map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, value := range settings {
		r.values[key] = value
	}
	return nil
}

func (r *dingTalkMemorySettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.values))
	for key, value := range r.values {
		out[key] = value
	}
	return out, nil
}

func (r *dingTalkMemorySettingRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

// =========================
// 告警正文（含恢复时间）
// =========================

func TestBuildOpsAlertDingTalkContentIncludesRecoveryTime(t *testing.T) {
	resetAt := time.Date(2026, 12, 30, 14, 12, 0, 0, time.UTC)
	opsSvc := &OpsService{
		getAccountAvailability: func(context.Context, string, *int64) (*OpsAccountAvailability, error) {
			return &OpsAccountAvailability{
				Group: &GroupAvailability{GroupID: 3, GroupName: "OpenAI", TotalAccounts: 2, AvailableCount: 0},
				Accounts: map[int64]*AccountAvailability{
					11: {
						AccountID:        11,
						AccountName:      "acc-01",
						Status:           StatusActive,
						IsAvailable:      false,
						IsRateLimited:    true,
						RateLimitResetAt: &resetAt,
					},
					12: {
						AccountID:              12,
						AccountName:            "acc-02",
						Status:                 StatusActive,
						IsAvailable:            false,
						TempUnschedulableUntil: &resetAt,
					},
				},
			}, nil
		},
	}
	svc := &OpsAlertEvaluatorService{opsService: opsSvc}

	metricValue := 0.0
	rule := &OpsAlertRule{
		ID:         7,
		Name:       "OpenAI 分组不可用",
		Severity:   "P1",
		MetricType: "group_available_accounts",
		Operator:   "<=",
		Threshold:  0,
		Filters:    map[string]any{"platform": "openai", "group_id": float64(3)},
	}
	event := &OpsAlertEvent{ID: 99, RuleID: 7, Status: OpsAlertStatusFiring, MetricValue: &metricValue}

	title, text := svc.buildOpsAlertDingTalkContent(context.Background(), rule, event, false)

	require.Contains(t, title, "OpenAI 分组不可用")
	require.Contains(t, title, "告警")
	require.Contains(t, text, "账号池告警")
	require.Contains(t, text, "acc-01")
	require.Contains(t, text, "acc-02")
	require.Contains(t, text, "上游限流")
	require.Contains(t, text, resetAt.In(timezone.Location()).Format("2006-01-02 15:04:05"))
	require.Contains(t, text, "用量触达停调阈值")
	require.Contains(t, text, "可用 0 / 共 2")
	require.Contains(t, text, "分组 id：3")
	require.NotContains(t, text, "{{")
}

func TestBuildOpsAlertDingTalkContentResolved(t *testing.T) {
	opsSvc := &OpsService{
		getAccountAvailability: func(context.Context, string, *int64) (*OpsAccountAvailability, error) {
			return &OpsAccountAvailability{
				Group: &GroupAvailability{GroupID: 3, GroupName: "Kimi", TotalAccounts: 1, AvailableCount: 1},
				Accounts: map[int64]*AccountAvailability{
					21: {AccountID: 21, AccountName: "kimi-01", Status: StatusActive, IsAvailable: true},
				},
			}, nil
		},
	}
	svc := &OpsAlertEvaluatorService{opsService: opsSvc}

	metricValue := 1.0
	rule := &OpsAlertRule{
		Name:       "Kimi 分组不可用",
		Severity:   "P1",
		MetricType: "group_available_accounts",
		Operator:   "<=",
		Threshold:  0,
		Filters:    map[string]any{"platform": "kimi", "group_id": float64(3)},
	}
	event := &OpsAlertEvent{ID: 100, RuleID: 8, Status: OpsAlertStatusResolved, MetricValue: &metricValue}

	title, text := svc.buildOpsAlertDingTalkContent(context.Background(), rule, event, true)

	require.Contains(t, title, "恢复")
	require.Contains(t, text, "账号池恢复")
	require.Contains(t, text, "已恢复可调度")
	require.Contains(t, text, "可用 1 / 共 1")
}

func TestMaybeSendDingTalkSkipsWhenDisabled(t *testing.T) {
	ctx := context.Background()
	rule := &OpsAlertRule{ID: 1, Name: "r", NotifyDingTalk: false}
	event := &OpsAlertEvent{ID: 2}

	// 未注入发送器 → 不发送
	svc := &OpsAlertEvaluatorService{}
	require.False(t, svc.maybeSendDingTalk(ctx, nil, rule, event, false))

	// 注入了发送器但规则未开启钉钉 → 不发送
	svc = &OpsAlertEvaluatorService{dingtalk: NewDingTalkNotifyService()}
	require.False(t, svc.maybeSendDingTalk(ctx, nil, rule, event, false))

	// 规则开启但没有 opsService（配置读不到）→ 不发送
	rule.NotifyDingTalk = true
	require.False(t, svc.maybeSendDingTalk(ctx, nil, rule, event, false))
}

func TestAccountUnavailableReasonFallsBack(t *testing.T) {
	require.Equal(t, "状态未知", accountUnavailableReason(nil))
	require.Contains(t, accountUnavailableReason(&AccountAvailability{Status: StatusActive}), "不可调度")
	require.Contains(t, accountUnavailableReason(&AccountAvailability{HasError: true, ErrorMessage: "401\ninvalid"}), "账号状态异常")
	require.NotContains(t, accountUnavailableReason(&AccountAvailability{HasError: true, ErrorMessage: "401\ninvalid"}), "\n")
}

func TestEscapeDingTalkMarkdownText(t *testing.T) {
	got := escapeDingTalkMarkdownText("a\nb*c#d`e")
	require.NotContains(t, got, "\n")
	require.NotContains(t, got, "*")
	require.NotContains(t, got, "#")
	require.NotContains(t, got, "`")
	require.True(t, strings.HasPrefix(got, "a b"))
}
