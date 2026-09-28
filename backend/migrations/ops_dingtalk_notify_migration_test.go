package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsDingTalkNotifyMigration(t *testing.T) {
	content, err := FS.ReadFile("241_ops_dingtalk_notify.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	// 规则级开关：默认关闭，避免升级后突然开始推送。
	require.Contains(t, sql, "ALTER TABLE ops_alert_rules ADD COLUMN IF NOT EXISTS notify_dingtalk BOOLEAN NOT NULL DEFAULT FALSE")

	// 事件级投递状态：与既有 email_sent 对齐。
	require.Contains(t, sql, "ALTER TABLE ops_alert_events ADD COLUMN IF NOT EXISTS dingtalk_sent BOOLEAN NOT NULL DEFAULT FALSE")
}
