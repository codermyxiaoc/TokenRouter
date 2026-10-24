//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type chanshuiIntentAccountRepo struct {
	updateAccountCredsRepoStub
	marked bool
}

func (r *chanshuiIntentAccountRepo) Update(ctx context.Context, a *Account) error {
	r.marked = HasChanshuiManualAccountState(ctx)
	return r.updateAccountCredsRepoStub.Update(ctx, a)
}

func (r *chanshuiIntentAccountRepo) SetSchedulable(ctx context.Context, _ int64, _ bool) error {
	r.marked = HasChanshuiManualAccountState(ctx)
	return nil
}

type chanshuiIntentBulkRepo struct {
	accountRepoStubForBulkUpdate
	marked bool
}

func (r *chanshuiIntentBulkRepo) BulkUpdate(ctx context.Context, ids []int64, update AccountBulkUpdate) (int64, error) {
	r.marked = HasChanshuiManualAccountState(ctx)
	return r.accountRepoStubForBulkUpdate.BulkUpdate(ctx, ids, update)
}

// 只有管理员明确传入状态/调度意图才撤销恢复资格，普通资料编辑不能误标后台维护。
func TestChanshuiManualAccountIntentOnlyForExplicitState(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		r := &chanshuiIntentAccountRepo{updateAccountCredsRepoStub: updateAccountCredsRepoStub{account: &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusDisabled, Credentials: map[string]any{}}}}
		svc := &adminServiceImpl{accountRepo: r}
		in := &UpdateAccountInput{Name: "edited"}
		if explicit {
			in.Status = StatusDisabled
		}
		_, err := svc.UpdateAccount(context.Background(), 7, in)
		require.NoError(t, err)
		require.Equal(t, explicit, r.marked)
		r.marked = false
		_, err = svc.SetAccountSchedulable(context.Background(), 7, false)
		require.NoError(t, err)
		require.True(t, r.marked)
	}
	for _, mode := range []string{"status", "schedulable", "unrelated"} {
		r := &chanshuiIntentBulkRepo{}
		svc := &adminServiceImpl{accountRepo: r}
		in := &BulkUpdateAccountsInput{AccountIDs: []int64{7}, Name: "edited"}
		if mode == "status" {
			in.Status = StatusDisabled
		}
		if mode == "schedulable" {
			v := false
			in.Schedulable = &v
		}
		_, err := svc.BulkUpdateAccounts(context.Background(), in)
		require.NoError(t, err)
		require.Equal(t, mode != "unrelated", r.marked)
	}
}
