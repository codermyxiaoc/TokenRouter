-- 邮箱归一化查重会从公开注册和发码路径调用；为与 Go 查询完全相同的表达式建立索引，
-- 避免用户表增长后退化为不可控的全表扫描。该索引只加速等值探测，不施加唯一约束，
-- 因为功能可由管理员关闭，且历史数据可能已存在别名重复。
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_registration_email_normalized
    ON users (((
        CASE
            WHEN rtrim(split_part(lower(btrim(email)), '@', 2), '.') IN ('gmail.com', 'googlemail.com') THEN
                coalesce(
                    nullif(
                        replace(
                            CASE
                                WHEN strpos(split_part(lower(btrim(email)), '@', 1), '+') > 1 THEN
                                    left(
                                        split_part(lower(btrim(email)), '@', 1),
                                        strpos(split_part(lower(btrim(email)), '@', 1), '+') - 1
                                    )
                                ELSE split_part(lower(btrim(email)), '@', 1)
                            END,
                            '.',
                            ''
                        ),
                        ''
                    ),
                    CASE
                        WHEN strpos(split_part(lower(btrim(email)), '@', 1), '+') > 1 THEN
                            left(
                                split_part(lower(btrim(email)), '@', 1),
                                strpos(split_part(lower(btrim(email)), '@', 1), '+') - 1
                            )
                        ELSE split_part(lower(btrim(email)), '@', 1)
                    END
                ) || '@gmail.com'
            ELSE
                CASE
                    WHEN strpos(split_part(lower(btrim(email)), '@', 1), '+') > 1 THEN
                        left(
                            split_part(lower(btrim(email)), '@', 1),
                            strpos(split_part(lower(btrim(email)), '@', 1), '+') - 1
                        )
                    ELSE split_part(lower(btrim(email)), '@', 1)
                END || '@' || rtrim(split_part(lower(btrim(email)), '@', 2), '.')
        END
    )))
    WHERE deleted_at IS NULL;
