//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateProfile_RejectsEmailBeforeEmailIdentityResync(t *testing.T) {
	repo := &emailSyncRepoStub{
		user: &User{
			ID:          19,
			Email:       "profile-before@example.com",
			Username:    "tester",
			Concurrency: 2,
		},
		replaceErr: context.DeadlineExceeded,
	}
	svc := NewUserService(repo, nil, nil, nil)

	newEmail := "profile-after@example.com"
	_, err := svc.UpdateProfile(context.Background(), 19, UpdateProfileRequest{
		Email: &newEmail,
	})
	require.ErrorIs(t, err, ErrProfileEmailChangeForbidden)
	require.Equal(t, 0, repo.updateCalls)
	require.Empty(t, repo.replaceCalls)
	require.Empty(t, repo.ensureCalls)
	require.Equal(t, "profile-before@example.com", repo.user.Email)
}
