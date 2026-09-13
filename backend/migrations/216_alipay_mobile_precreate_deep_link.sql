-- 默认保留移动端支付宝 WAP 流程，仅在显式开启后使用预下单深链接。
INSERT INTO settings (key, value, updated_at)
VALUES ('ALIPAY_MOBILE_PRECREATE_DEEP_LINK', 'false', NOW())
ON CONFLICT (key) DO NOTHING;
