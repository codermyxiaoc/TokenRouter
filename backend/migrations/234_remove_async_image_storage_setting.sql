-- 移除自研异步图片任务，清理可能包含对象存储密钥的运行时设置，避免保留失效秘密。
DELETE FROM settings WHERE key = 'image_storage_config';
