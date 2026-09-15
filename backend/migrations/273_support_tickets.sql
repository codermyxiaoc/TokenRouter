-- 工单正文、对话和附件在 PostgreSQL 中共同持久化，避免对象上传成功但事务失败留下孤儿文件。
CREATE TABLE IF NOT EXISTS support_tickets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    type VARCHAR(20) NOT NULL CHECK (type IN ('presales', 'aftersales', 'technical', 'financial', 'other')),
    title VARCHAR(200) NOT NULL,
    content TEXT NOT NULL,
    priority VARCHAR(10) NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'waiting_user', 'completed', 'cancelled', 'expired')),
    order_id BIGINT REFERENCES payment_orders(id),
    assigned_to BIGINT REFERENCES users(id) ON DELETE SET NULL,
    last_staff_reply_at TIMESTAMPTZ,
    closed_at TIMESTAMPTZ,
    request_key VARCHAR(128) NOT NULL DEFAULT '',
    request_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT support_tickets_order_type_check CHECK (type = 'financial' OR order_id IS NULL)
);
CREATE INDEX IF NOT EXISTS idx_support_tickets_user_status ON support_tickets(user_id, status);
CREATE INDEX IF NOT EXISTS idx_support_tickets_queue ON support_tickets(status, priority, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_support_tickets_expiry ON support_tickets(last_staff_reply_at) WHERE status = 'waiting_user';
CREATE UNIQUE INDEX IF NOT EXISTS idx_support_tickets_request ON support_tickets(user_id, request_key) WHERE request_key <> '';

CREATE TABLE IF NOT EXISTS support_ticket_messages (
    id BIGSERIAL PRIMARY KEY,
    ticket_id BIGINT NOT NULL REFERENCES support_tickets(id),
    user_id BIGINT NOT NULL REFERENCES users(id),
    is_staff BOOLEAN NOT NULL DEFAULT FALSE,
    content TEXT NOT NULL,
    request_key VARCHAR(128) NOT NULL DEFAULT '',
    request_hash VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_support_ticket_messages_ticket ON support_ticket_messages(ticket_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_support_ticket_messages_request ON support_ticket_messages(ticket_id, user_id, request_key) WHERE request_key <> '';

CREATE TABLE IF NOT EXISTS support_ticket_attachments (
    id BIGSERIAL PRIMARY KEY,
    message_id BIGINT NOT NULL REFERENCES support_ticket_messages(id),
    filename VARCHAR(255) NOT NULL,
    content_type VARCHAR(150) NOT NULL,
    size BIGINT NOT NULL CHECK (size > 0 AND size <= 20971520),
    data BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT support_ticket_attachment_size_check CHECK (octet_length(data) = size)
);
CREATE INDEX IF NOT EXISTS idx_support_ticket_attachments_message ON support_ticket_attachments(message_id);
