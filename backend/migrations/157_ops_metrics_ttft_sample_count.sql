-- 为 ops_metrics_hourly / ops_metrics_daily 增加 TTFT 样本数。
--
-- first_token_ms 只在流式请求中记录。历史逻辑用 success_count 合并 TTFT
-- 分位数，会把非流式成功请求也计入权重，导致长时间范围的运维监控 TTFT 被稀释。
--
-- 新列记录每个聚合桶内真实参与 TTFT 统计的样本数，使后续 hourly/daily
-- 汇总按流式样本数加权。已有数据默认为 0，后续预聚合重算时会回填。

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

ALTER TABLE ops_metrics_hourly
    ADD COLUMN IF NOT EXISTS ttft_sample_count BIGINT NOT NULL DEFAULT 0;

ALTER TABLE ops_metrics_daily
    ADD COLUMN IF NOT EXISTS ttft_sample_count BIGINT NOT NULL DEFAULT 0;
