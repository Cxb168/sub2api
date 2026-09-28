-- 运维告警钉钉通道：规则级开关 + 事件级投递状态。
-- 规则上的 notify_dingtalk 同时覆盖"触发"与"恢复"两条消息；
-- 是否发送恢复消息由全局配置 ops_dingtalk_notification_config.include_resolved_alerts 控制。
ALTER TABLE ops_alert_rules
    ADD COLUMN IF NOT EXISTS notify_dingtalk BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE ops_alert_events
    ADD COLUMN IF NOT EXISTS dingtalk_sent BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN ops_alert_rules.notify_dingtalk IS '该告警规则命中时是否推送钉钉（含恢复通知，恢复由全局配置控制）';
COMMENT ON COLUMN ops_alert_events.dingtalk_sent IS '该事件是否已成功投递钉钉消息（告警或恢复）';
