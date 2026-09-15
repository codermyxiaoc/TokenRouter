-- 将紧急优先级合并为高，保留历史工单、消息、附件、分配记录及请求幂等摘要。
ALTER TABLE support_tickets DROP CONSTRAINT IF EXISTS support_tickets_priority_check;
UPDATE support_tickets SET priority = 'high' WHERE priority = 'urgent';
ALTER TABLE support_tickets ADD CONSTRAINT support_tickets_priority_check
    CHECK (priority IN ('low', 'normal', 'high'));

-- 兼容升级窗口旧程序的紧急优先级写入，落库始终保持三个正式级别。
CREATE OR REPLACE FUNCTION normalize_support_ticket_priority() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.priority = 'urgent' THEN
        NEW.priority := 'high';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS support_ticket_normalize_priority ON support_tickets;
CREATE TRIGGER support_ticket_normalize_priority
    BEFORE INSERT OR UPDATE OF priority ON support_tickets
    FOR EACH ROW EXECUTE FUNCTION normalize_support_ticket_priority();
