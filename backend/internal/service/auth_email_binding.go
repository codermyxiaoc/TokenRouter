package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/ent/authidentity"
	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/TokenFlux/TokenRouter/internal/pkg/logger"
)

type normalizedEmailBindingConflictChecker interface {
	ExistsByNormalizedEmailExcluding(ctx context.Context, normalizedEmail string, excludedUserID int64) (bool, error)
}

// emailIdentityAliasGuardRepository 提供换绑主邮箱所需的事务内原子写入能力。
// 通过可选接口接入，保留无数据库测试桩的兼容性。
type emailIdentityAliasGuardRepository interface {
	UpdateEmailWithAliasGuard(ctx context.Context, userID int64, email string, passwordHash string) error
}

// BindEmailIdentity verifies and binds a local email/password identity to the
// current user, or replaces the existing bound primary email.
func (s *AuthService) BindEmailIdentity(
	ctx context.Context,
	userID int64,
	email string,
	verifyCode string,
	password string,
) (*User, error) {
	if s == nil {
		return nil, ErrServiceUnavailable
	}

	normalizedEmail, err := normalizeEmailForIdentityBinding(email)
	if err != nil {
		return nil, err
	}
	if isReservedEmail(normalizedEmail) {
		return nil, ErrEmailReserved
	}
	if strings.TrimSpace(password) == "" {
		return nil, ErrPasswordRequired
	}
	currentUser, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureUserEmailChangeAllowed(ctx, currentUser, normalizedEmail); err != nil {
		return nil, err
	}
	if err := s.VerifyOAuthEmailCode(ctx, normalizedEmail, verifyCode); err != nil {
		return nil, err
	}
	if err := s.validateRegistrationEmailPolicy(ctx, normalizedEmail); err != nil {
		return nil, err
	}
	firstRealEmailBind := !hasBindableEmailIdentitySubject(currentUser.Email)
	if firstRealEmailBind && len(password) < 6 {
		return nil, infraerrors.BadRequest("PASSWORD_TOO_SHORT", "password must be at least 6 characters")
	}
	if !firstRealEmailBind && !s.CheckPassword(password, currentUser.PasswordHash) {
		return nil, ErrPasswordIncorrect
	}

	registrationNormalizedEmail := s.normalizeRegistrationEmailForBinding(ctx, normalizedEmail)
	if err := s.ensureEmailBindingTargetAvailable(ctx, currentUser, normalizedEmail, registrationNormalizedEmail); err != nil {
		return nil, err
	}

	hashedPassword, err := s.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	if s.entClient != nil {
		if err := s.updateBoundEmailIdentityTx(ctx, currentUser, normalizedEmail, registrationNormalizedEmail, hashedPassword, firstRealEmailBind); err != nil {
			return nil, err
		}
		s.revokeEmailIdentitySessions(ctx, userID)
		return currentUser, nil
	}

	currentUser.Email = normalizedEmail
	currentUser.PasswordHash = hashedPassword
	fields := UserUpdateFields{Email: true, PasswordHash: true}
	updateUser := s.userRepo.Update
	if registrationNormalizedEmail != "" {
		// 开启邮箱归一化后，绑定/换绑主邮箱也要复用同一套唯一性保护。
		updateUser = func(updateCtx context.Context, updateUser *User, updateFields UserUpdateFields) error {
			return s.userRepo.UpdateWithNormalizedEmailGuard(updateCtx, updateUser, registrationNormalizedEmail, updateFields)
		}
	}
	if err := updateUser(ctx, currentUser, fields); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return nil, ErrEmailExists
		}
		return nil, ErrServiceUnavailable
	}

	if firstRealEmailBind {
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, userID, "email"); err != nil {
			return nil, fmt.Errorf("apply email first bind defaults: %w", err)
		}
	}

	s.revokeEmailIdentitySessions(ctx, userID)
	return currentUser, nil
}

// SendEmailIdentityBindCode sends a verification code for authenticated email binding flows.
func (s *AuthService) SendEmailIdentityBindCode(ctx context.Context, userID int64, email string, locale ...string) error {
	if s == nil {
		return ErrServiceUnavailable
	}

	normalizedEmail, err := normalizeEmailForIdentityBinding(email)
	if err != nil {
		return err
	}
	if isReservedEmail(normalizedEmail) {
		return ErrEmailReserved
	}
	currentUser, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return ErrUserNotFound
		}
		return ErrServiceUnavailable
	}
	if err := s.ensureUserEmailChangeAllowed(ctx, currentUser, normalizedEmail); err != nil {
		return err
	}
	if err := s.validateRegistrationEmailPolicy(ctx, normalizedEmail); err != nil {
		return err
	}
	if s.emailService == nil {
		return ErrServiceUnavailable
	}
	registrationNormalizedEmail := s.normalizeRegistrationEmailForBinding(ctx, normalizedEmail)
	if err := s.ensureEmailBindingTargetAvailable(ctx, currentUser, normalizedEmail, registrationNormalizedEmail); err != nil {
		return err
	}

	siteName := "Sub2API"
	if s.settingService != nil {
		siteName = s.settingService.GetSiteName(ctx)
	}
	return s.emailService.SendVerifyCode(ctx, normalizedEmail, siteName, firstEmailLocale(locale))
}

// ensureUserEmailChangeAllowed 只限制真实邮箱发生变化；首次绑定或验证现有邮箱仍保持可用。
func (s *AuthService) ensureUserEmailChangeAllowed(ctx context.Context, currentUser *User, targetEmail string) error {
	if currentUser == nil {
		return ErrUserNotFound
	}
	if !hasBindableEmailIdentitySubject(currentUser.Email) || strings.EqualFold(strings.TrimSpace(currentUser.Email), targetEmail) {
		return nil
	}
	if s.settingService == nil || !s.settingService.IsUserEmailChangeEnabled(ctx) {
		return ErrEmailChangeDisabled
	}
	return nil
}

func normalizeEmailForIdentityBinding(email string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || len(normalized) > 255 {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	if _, err := mail.ParseAddress(normalized); err != nil {
		return "", infraerrors.BadRequest("INVALID_EMAIL", "invalid email")
	}
	return normalized, nil
}

func hasBindableEmailIdentitySubject(email string) bool {
	normalized := strings.ToLower(strings.TrimSpace(email))
	return normalized != "" && !isReservedEmail(normalized)
}

func (s *AuthService) normalizeRegistrationEmailForBinding(ctx context.Context, email string) string {
	if s == nil || s.settingService == nil || !s.settingService.IsRegistrationEmailNormalizationEnabled(ctx) {
		return ""
	}
	return NormalizeRegistrationEmailAddress(email)
}

func (s *AuthService) ensureEmailBindingTargetAvailable(
	ctx context.Context,
	currentUser *User,
	email string,
	registrationNormalizedEmail string,
) error {
	existingUser, err := s.userRepo.GetByEmail(ctx, email)
	switch {
	case err == nil:
		if existingUser != nil && (currentUser == nil || existingUser.ID != currentUser.ID) {
			return ErrEmailExists
		}
	case err != nil && !errors.Is(err, ErrUserNotFound):
		return ErrServiceUnavailable
	}

	if err := s.ensureEmailAliasTargetAvailable(ctx, currentUser, email); err != nil {
		return err
	}

	if registrationNormalizedEmail == "" {
		return nil
	}

	exists, err := s.hasNormalizedEmailBindingConflict(ctx, currentUser, registrationNormalizedEmail)
	if err != nil {
		return ErrServiceUnavailable
	}
	if exists {
		return ErrEmailExists
	}
	return nil
}

// ensureEmailAliasTargetAvailable 检查邮箱是否已被其他用户的收件箱身份占用。
// 真实仓储提供 owner 查询，因此当前用户把自己的邮箱换成另一个 alias 时不会被误拒；
// 只有旧仓储仅提供布尔查询时才采取保守拒绝策略。
func (s *AuthService) ensureEmailAliasTargetAvailable(ctx context.Context, currentUser *User, email string) error {
	if currentUser == nil {
		return ErrUserNotFound
	}
	if lookup, ok := s.userRepo.(emailAliasOwnerLookupRepository); ok {
		ownerID, exists, err := lookup.EmailAliasOwnerID(ctx, email, currentUser.ID)
		if err != nil {
			return ErrServiceUnavailable
		}
		if exists && ownerID != currentUser.ID {
			return ErrEmailExists
		}
		return nil
	}
	lookup, ok := s.userRepo.(emailAliasLookupRepository)
	if !ok {
		return nil
	}
	exists, err := lookup.ExistsByEmailAlias(ctx, email)
	if err != nil {
		return ErrServiceUnavailable
	}
	if exists {
		// 无法区分当前用户时拒绝，避免并发或历史重复数据造成占用绕过。
		return ErrEmailExists
	}
	return nil
}

func (s *AuthService) hasNormalizedEmailBindingConflict(
	ctx context.Context,
	currentUser *User,
	registrationNormalizedEmail string,
) (bool, error) {
	if registrationNormalizedEmail == "" {
		return false, nil
	}

	currentUserID := int64(0)
	currentNormalizedEmail := ""
	if currentUser != nil {
		currentUserID = currentUser.ID
		currentNormalizedEmail = NormalizeRegistrationEmailAddress(currentUser.Email)
	}
	if currentUserID > 0 && currentNormalizedEmail == registrationNormalizedEmail {
		if checker, ok := s.userRepo.(normalizedEmailBindingConflictChecker); ok {
			return checker.ExistsByNormalizedEmailExcluding(ctx, registrationNormalizedEmail, currentUserID)
		}
		// 降级路径无法排除当前用户自身，保留原有宽松行为，避免把自己误判为冲突。
		return false, nil
	}

	return s.userRepo.ExistsByNormalizedEmail(ctx, registrationNormalizedEmail)
}

func (s *AuthService) updateBoundEmailIdentityTx(
	ctx context.Context,
	currentUser *User,
	email string,
	registrationNormalizedEmail string,
	hashedPassword string,
	applyFirstBindDefaults bool,
) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return s.updateBoundEmailIdentityWithClient(ctx, tx.Client(), currentUser, email, registrationNormalizedEmail, hashedPassword, applyFirstBindDefaults)
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)
	if err := s.updateBoundEmailIdentityWithClient(txCtx, tx.Client(), currentUser, email, registrationNormalizedEmail, hashedPassword, applyFirstBindDefaults); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrServiceUnavailable
	}
	return nil
}

func (s *AuthService) updateBoundEmailIdentityWithClient(
	ctx context.Context,
	client *dbent.Client,
	currentUser *User,
	email string,
	registrationNormalizedEmail string,
	hashedPassword string,
	applyFirstBindDefaults bool,
) error {
	if client == nil || currentUser == nil || currentUser.ID <= 0 {
		return ErrServiceUnavailable
	}

	oldEmail := currentUser.Email
	if guard, ok := s.userRepo.(emailIdentityAliasGuardRepository); ok {
		// guard 在同一事务内锁定别名身份并复查，关闭前置查重与写入之间的窗口。
		if err := guard.UpdateEmailWithAliasGuard(ctx, currentUser.ID, email, hashedPassword); err != nil {
			return err
		}
	} else {
		// 兼容尚未实现别名 guard 的测试桩，保留 fork 原有的归一化唯一性保护。
		updatedUser := *currentUser
		updatedUser.Email = email
		updatedUser.PasswordHash = hashedPassword
		if registrationNormalizedEmail != "" {
			if err := s.userRepo.LockRegistrationEmail(ctx, registrationNormalizedEmail); err != nil {
				return ErrServiceUnavailable
			}
			exists, err := s.hasNormalizedEmailBindingConflict(ctx, currentUser, registrationNormalizedEmail)
			if err != nil {
				return ErrServiceUnavailable
			}
			if exists {
				return ErrEmailExists
			}
		}
		if _, err := client.User.UpdateOneID(currentUser.ID).
			SetEmail(updatedUser.Email).
			SetPasswordHash(updatedUser.PasswordHash).
			Save(ctx); err != nil {
			if dbent.IsConstraintError(err) || errors.Is(err, ErrEmailExists) {
				return ErrEmailExists
			}
			return ErrServiceUnavailable
		}
	}

	if err := replaceBoundEmailAuthIdentityWithClient(ctx, client, currentUser.ID, oldEmail, email, "auth_service_email_bind"); err != nil {
		if errors.Is(err, ErrEmailExists) {
			return ErrEmailExists
		}
		return ErrServiceUnavailable
	}

	if applyFirstBindDefaults {
		if err := s.ApplyProviderDefaultSettingsOnFirstBind(ctx, currentUser.ID, "email"); err != nil {
			return fmt.Errorf("apply email first bind defaults: %w", err)
		}
	}

	refreshedUser, err := client.User.Get(ctx, currentUser.ID)
	if err != nil {
		return ErrServiceUnavailable
	}
	currentUser.Email = refreshedUser.Email
	currentUser.PasswordHash = refreshedUser.PasswordHash
	currentUser.Balance = refreshedUser.Balance
	currentUser.Concurrency = refreshedUser.Concurrency
	currentUser.UpdatedAt = refreshedUser.UpdatedAt
	return nil
}

func (s *AuthService) revokeEmailIdentitySessions(ctx context.Context, userID int64) {
	if err := s.RevokeAllUserSessions(ctx, userID); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to revoke refresh sessions after email identity bind for user %d: %v", userID, err)
	}
}

func replaceBoundEmailAuthIdentityWithClient(
	ctx context.Context,
	client *dbent.Client,
	userID int64,
	oldEmail string,
	newEmail string,
	source string,
) error {
	newSubject := normalizeBoundEmailAuthIdentitySubject(newEmail)
	if err := ensureBoundEmailAuthIdentityWithClient(ctx, client, userID, newSubject, source); err != nil {
		return err
	}

	oldSubject := normalizeBoundEmailAuthIdentitySubject(oldEmail)
	if oldSubject == "" || oldSubject == newSubject {
		return nil
	}

	_, err := client.AuthIdentity.Delete().
		Where(
			authidentity.UserIDEQ(userID),
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(oldSubject),
		).
		Exec(ctx)
	return err
}

func ensureBoundEmailAuthIdentityWithClient(
	ctx context.Context,
	client *dbent.Client,
	userID int64,
	subject string,
	source string,
) error {
	if client == nil || userID <= 0 || subject == "" {
		return nil
	}

	if strings.TrimSpace(source) == "" {
		source = "auth_service_email_bind"
	}

	if err := client.AuthIdentity.Create().
		SetUserID(userID).
		SetProviderType("email").
		SetProviderKey("email").
		SetProviderSubject(subject).
		SetVerifiedAt(time.Now().UTC()).
		SetMetadata(map[string]any{"source": strings.TrimSpace(source)}).
		OnConflictColumns(
			authidentity.FieldProviderType,
			authidentity.FieldProviderKey,
			authidentity.FieldProviderSubject,
		).
		DoNothing().
		Exec(ctx); err != nil {
		if !isSQLNoRowsError(err) {
			return err
		}
	}

	identity, err := client.AuthIdentity.Query().
		Where(
			authidentity.ProviderTypeEQ("email"),
			authidentity.ProviderKeyEQ("email"),
			authidentity.ProviderSubjectEQ(subject),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil
		}
		return err
	}
	if identity.UserID != userID {
		return ErrEmailExists
	}
	return nil
}

func normalizeBoundEmailAuthIdentitySubject(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || isReservedEmail(normalized) {
		return ""
	}
	return normalized
}
