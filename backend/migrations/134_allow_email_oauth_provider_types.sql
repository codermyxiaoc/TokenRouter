ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_signup_source_check;

ALTER TABLE users
    ADD CONSTRAINT users_signup_source_check
    CHECK (signup_source IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google'));

ALTER TABLE auth_identities
    DROP CONSTRAINT IF EXISTS auth_identities_provider_type_check;

ALTER TABLE auth_identities
    ADD CONSTRAINT auth_identities_provider_type_check
    CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google'));

ALTER TABLE auth_identity_channels
    DROP CONSTRAINT IF EXISTS auth_identity_channels_provider_type_check;

ALTER TABLE auth_identity_channels
    ADD CONSTRAINT auth_identity_channels_provider_type_check
    CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google'));

ALTER TABLE pending_auth_sessions
    DROP CONSTRAINT IF EXISTS pending_auth_sessions_provider_type_check;

ALTER TABLE pending_auth_sessions
    ADD CONSTRAINT pending_auth_sessions_provider_type_check
    CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google'));

ALTER TABLE user_provider_default_grants
    DROP CONSTRAINT IF EXISTS user_provider_default_grants_provider_type_check;

ALTER TABLE user_provider_default_grants
    ADD CONSTRAINT user_provider_default_grants_provider_type_check
    CHECK (provider_type IN ('email', 'linuxdo', 'wechat', 'oidc', 'github', 'google'));

INSERT INTO settings (key, value)
VALUES
    ('github_oauth_enabled', 'false'),
    ('github_oauth_client_id', ''),
    ('github_oauth_client_secret', ''),
    ('github_oauth_redirect_url', ''),
    ('github_oauth_frontend_redirect_url', '/auth/oauth/callback'),
    ('google_oauth_enabled', 'false'),
    ('google_oauth_client_id', ''),
    ('google_oauth_client_secret', ''),
    ('google_oauth_redirect_url', ''),
    ('google_oauth_frontend_redirect_url', '/auth/oauth/callback'),
    ('auth_source_default_github_balance', '0'),
    ('auth_source_default_github_concurrency', '5'),
    ('auth_source_default_github_subscriptions', '[]'),
    ('auth_source_default_github_grant_on_signup', 'false'),
    ('auth_source_default_github_grant_on_first_bind', 'false'),
    ('auth_source_default_google_balance', '0'),
    ('auth_source_default_google_concurrency', '5'),
    ('auth_source_default_google_subscriptions', '[]'),
    ('auth_source_default_google_grant_on_signup', 'false'),
    ('auth_source_default_google_grant_on_first_bind', 'false')
ON CONFLICT (key) DO NOTHING;
