package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 运维告警钉钉机器人通道。
//
// 只对接官方自定义机器人 Webhook（https://oapi.dingtalk.com/robot/send），
// 安全设置支持"加签"：timestamp(毫秒) + "\n" + secret 做 HMAC-SHA256，Base64 后 URL 编码追加到 query。
// 官方限制单机器人 20 条/分钟（超限封禁 10 分钟），所以发送侧有滑动窗口限流，
// 并且由调用方（告警评估器）保证同一事件只投递一次。
const (
	dingTalkWebhookHost    = "oapi.dingtalk.com"
	dingTalkWebhookPath    = "/robot/send"
	dingTalkSendTimeout    = 10 * time.Second
	dingTalkMaxResponseLen = 8 * 1024
)

// DingTalkNotifyService 负责把 markdown 消息投递到钉钉机器人。
type DingTalkNotifyService struct {
	client  *http.Client
	limiter *slidingWindowLimiter
}

// NewDingTalkNotifyService 构造发送服务。
func NewDingTalkNotifyService() *DingTalkNotifyService {
	return &DingTalkNotifyService{
		client: &http.Client{
			Timeout: dingTalkSendTimeout,
			// 官方地址不需要跳转；禁止重定向，避免被引导到任意 host（SSRF 面）。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		limiter: newSlidingWindowLimiter(0, time.Hour),
	}
}

// SetLimit 更新逐小时发送上限（0 表示不限制）。
func (s *DingTalkNotifyService) SetLimit(limitPerHour int) {
	if s == nil || s.limiter == nil {
		return
	}
	s.limiter.SetLimit(limitPerHour)
}

// Allow 按滑动窗口判断当前是否还可以发送。
func (s *DingTalkNotifyService) Allow(now time.Time) bool {
	if s == nil || s.limiter == nil {
		return true
	}
	return s.limiter.Allow(now)
}

// ValidateDingTalkWebhookURL 校验必须是指定 host + 路径的官方 HTTPS 地址且带 access_token。
func ValidateDingTalkWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("dingtalk webhook url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("dingtalk webhook url is invalid")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("dingtalk webhook url must use https")
	}
	if !strings.EqualFold(parsed.Host, dingTalkWebhookHost) {
		return fmt.Errorf("dingtalk webhook host must be %s", dingTalkWebhookHost)
	}
	if strings.TrimRight(parsed.Path, "/") != dingTalkWebhookPath {
		return fmt.Errorf("dingtalk webhook path must be %s", dingTalkWebhookPath)
	}
	if strings.TrimSpace(parsed.Query().Get("access_token")) == "" {
		return errors.New("dingtalk webhook url must contain access_token")
	}
	return nil
}

// BuildSignedWebhookURL 在开启加签时返回带 timestamp/sign 的地址；未配置 secret 时原样返回。
func BuildSignedWebhookURL(webhook string, secret string, now time.Time) (string, error) {
	webhook = strings.TrimSpace(webhook)
	if err := ValidateDingTalkWebhookURL(webhook); err != nil {
		return "", err
	}
	secret = strings.TrimSpace(secret)

	parsed, err := url.Parse(webhook)
	if err != nil {
		return "", errors.New("dingtalk webhook url is invalid")
	}
	if secret == "" {
		return parsed.String(), nil
	}

	if now.IsZero() {
		now = time.Now()
	}
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	q := parsed.Query()
	q.Set("timestamp", timestamp)
	q.Set("sign", sign)
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

// SendMarkdown 投递一条 markdown 消息。成功返回 nil；钉钉 errcode != 0 时返回带 errcode 的错误。
func (s *DingTalkNotifyService) SendMarkdown(ctx context.Context, cfg *OpsDingTalkNotificationConfig, title string, text string) error {
	if s == nil {
		return errors.New("dingtalk notify service is not initialized")
	}
	if cfg == nil {
		return errors.New("dingtalk config is nil")
	}
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		return errors.New("dingtalk webhook is not configured")
	}

	target, err := BuildSignedWebhookURL(cfg.WebhookURL, cfg.Secret, time.Now())
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	payload, err := BuildDingTalkMarkdownPayload(cfg, title, text)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, dingTalkMaxResponseLen))
	return ParseDingTalkSendResponse(resp.StatusCode, raw)
}

// BuildDingTalkMarkdownPayload 组装 markdown 消息体（含 @ 设置）。
func BuildDingTalkMarkdownPayload(cfg *OpsDingTalkNotificationConfig, title string, text string) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("dingtalk config is nil")
	}
	atMobiles := cfg.AtMobiles
	if atMobiles == nil {
		atMobiles = []string{}
	}
	payload := map[string]any{
		"msgtype": "markdown",
		"markdown": map[string]any{
			"title": strings.TrimSpace(title),
			"text":  text,
		},
		"at": map[string]any{
			"atMobiles": append([]string{}, atMobiles...),
			"isAtAll":   cfg.AtAll,
		},
	}
	return json.Marshal(payload)
}

// ParseDingTalkSendResponse 解析钉钉返回：HTTP 2xx 且 errcode == 0 才算成功。
func ParseDingTalkSendResponse(statusCode int, raw []byte) error {
	if statusCode < 200 || statusCode >= 300 {
		return fmt.Errorf("dingtalk http status %d", statusCode)
	}
	var parsed struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return errors.New("dingtalk response is not valid json")
	}
	if parsed.ErrCode != 0 {
		return fmt.Errorf("dingtalk errcode %d: %s", parsed.ErrCode, strings.TrimSpace(parsed.ErrMsg))
	}
	return nil
}

// Verify 供"测试消息"接口使用：先做地址校验，再真实投递。
func (s *DingTalkNotifyService) Verify(ctx context.Context, cfg *OpsDingTalkNotificationConfig, title string, text string) error {
	if cfg == nil {
		return errors.New("dingtalk config is nil")
	}
	if err := ValidateDingTalkWebhookURL(cfg.WebhookURL); err != nil {
		return err
	}
	return s.SendMarkdown(ctx, cfg, title, text)
}

// dingTalkTestMessageMu 防止测试消息接口被并发刷屏（打满 20 条/分钟会封禁 10 分钟）。
var dingTalkTestMessageMu sync.Mutex

// SendTestMessage 发送一条测试消息（低频，串行化）。
func (s *DingTalkNotifyService) SendTestMessage(ctx context.Context, cfg *OpsDingTalkNotificationConfig, siteName string) error {
	if s == nil {
		return errors.New("dingtalk notify service is not initialized")
	}
	dingTalkTestMessageMu.Lock()
	defer dingTalkTestMessageMu.Unlock()

	name := strings.TrimSpace(siteName)
	if name == "" {
		name = defaultSiteName
	}
	text := fmt.Sprintf("### ✅ 钉钉告警通道测试\n> 来源：%s\n\n如果你看到这条消息，说明 Webhook 与加签配置正确。\n\n**发送时间**：%s",
		name, time.Now().Format("2006-01-02 15:04:05"))
	return s.Verify(ctx, cfg, "钉钉告警通道测试", text)
}
