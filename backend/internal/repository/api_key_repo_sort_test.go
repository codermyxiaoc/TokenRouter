package repository

import (
	"context"
	"testing"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	"github.com/TokenFlux/TokenRouter/internal/pkg/pagination"
	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRepositoryListByUserIDSortByGroup(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "group-sort@test.com")
	otherUser := mustCreateAPIKeyRepoUser(t, ctx, client, "other-group-sort@test.com")
	createGroup := func(name string) *dbent.Group {
		g, err := client.Group.Create().SetName(name).Save(ctx)
		require.NoError(t, err)
		return g
	}
	// 逆序创建分组，防止错误地按分组 ID 排序也通过测试。
	zulu := createGroup("Zulu")
	alpha := createGroup("Alpha")
	createKey := func(userID int64, name string, groupID *int64, status string) int64 {
		key := &service.APIKey{
			UserID: userID, Key: "sk-" + name, Name: name, GroupID: groupID, Status: status,
		}
		require.NoError(t, repo.Create(ctx, key))
		return key.ID
	}
	zuluFirst := createKey(user.ID, "match-zulu-first", &zulu.ID, service.StatusActive)
	ungroupedFirst := createKey(user.ID, "match-ungrouped-first", nil, service.StatusActive)
	alphaFirst := createKey(user.ID, "match-alpha-first", &alpha.ID, service.StatusActive)
	zuluSecond := createKey(user.ID, "match-zulu-second", &zulu.ID, service.StatusActive)
	alphaSecond := createKey(user.ID, "match-alpha-second", &alpha.ID, service.StatusActive)
	ungroupedSecond := createKey(user.ID, "match-ungrouped-second", nil, service.StatusActive)
	createKey(otherUser.ID, "match-other-user", &alpha.ID, service.StatusActive)
	createKey(user.ID, "excluded-search", &alpha.ID, service.StatusActive)
	createKey(user.ID, "match-inactive", &alpha.ID, service.StatusDisabled)
	deleted := createKey(user.ID, "match-deleted", &alpha.ID, service.StatusActive)
	require.NoError(t, repo.Delete(ctx, deleted))

	ungroupedID := int64(0)
	for _, tc := range []struct {
		name    string
		order   string
		groupID *int64
		want    []int64
	}{
		{"ascending", "asc", nil, []int64{alphaFirst, alphaSecond, zuluFirst, zuluSecond, ungroupedFirst, ungroupedSecond}},
		{"descending", "desc", nil, []int64{zuluSecond, zuluFirst, alphaSecond, alphaFirst, ungroupedSecond, ungroupedFirst}},
		{"group filter", "asc", &alpha.ID, []int64{alphaFirst, alphaSecond}},
		{"ungrouped filter", "desc", &ungroupedID, []int64{ungroupedSecond, ungroupedFirst}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []int64
			const pageSize = 3
			pages := (len(tc.want) + pageSize - 1) / pageSize
			for page := 1; page <= pages; page++ {
				keys, result, err := repo.ListByUserID(ctx, user.ID, pagination.PaginationParams{
					Page: page, PageSize: pageSize, SortBy: "group", SortOrder: tc.order,
				}, service.APIKeyListFilters{Search: "match", Status: service.StatusActive, GroupID: tc.groupID})
				require.NoError(t, err)
				require.EqualValues(t, len(tc.want), result.Total)
				require.Equal(t, pages, result.Pages)
				for _, key := range keys {
					got = append(got, key.ID)
					if key.GroupID != nil {
						require.NotNil(t, key.Group, "sorting must preserve group preloading")
					}
				}
			}
			require.Equal(t, tc.want, got)
		})
	}
}

// 多分组按配置顺序中的首组排列，保留团队、托管和分组筛选边界。
func TestAPIKeyRepositorySortByGroupKeepsCompositeSmartAndTeamScope(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "multi-group-sort@test.com")
	zulu, err := client.Group.Create().SetName("Zulu").Save(ctx)
	require.NoError(t, err)
	alpha, err := client.Group.Create().SetName("Alpha").Save(ctx)
	require.NoError(t, err)
	team, err := client.Team.Create().SetName("sorting-team").Save(ctx)
	require.NoError(t, err)
	composite := &service.APIKey{UserID: user.ID, Key: "sk-sort-composite", Name: "composite", Status: service.StatusActive, IsComposite: true,
		CompositeGroups: []service.APIKeyCompositeGroup{{GroupID: zulu.ID, Prefix: "z", NormalizedPrefix: "z", SortOrder: 0}, {GroupID: alpha.ID, Prefix: "a", NormalizedPrefix: "a", SortOrder: 1}}}
	smart := &service.APIKey{UserID: user.ID, Key: "sk-sort-smart", Name: "smart", Status: service.StatusActive, SmartRouting: true,
		CompositeGroups: []service.APIKeyCompositeGroup{{GroupID: alpha.ID, Prefix: "a", NormalizedPrefix: "a", SortOrder: 0}, {GroupID: zulu.ID, Prefix: "z", NormalizedPrefix: "z", SortOrder: 1}}}
	ordinary := &service.APIKey{UserID: user.ID, Key: "sk-sort-ordinary", Name: "ordinary", Status: service.StatusActive, GroupID: &alpha.ID}
	teamKey := &service.APIKey{UserID: user.ID, Key: "sk-sort-team", Name: "team", Status: service.StatusActive, GroupID: &alpha.ID, TeamID: &team.ID}
	for _, key := range []*service.APIKey{composite, smart, ordinary, teamKey} {
		require.NoError(t, repo.Create(ctx, key))
	}
	_, err = client.APIKey.Create().SetUserID(user.ID).SetKey("sk-sort-managed").SetName("managed").SetGroupID(alpha.ID).SetManagedBy("test").Save(ctx)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, scope string
		groupID     *int64
		want        []int64
	}{
		{"all", "", nil, []int64{smart.ID, ordinary.ID, teamKey.ID, composite.ID}},
		{"personal", "personal", nil, []int64{smart.ID, ordinary.ID, composite.ID}},
		{"team", "team", nil, []int64{teamKey.ID}},
		{"composite group membership", "", &zulu.ID, []int64{smart.ID, composite.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := repo.ListByUserID(ctx, user.ID, pagination.PaginationParams{Page: 1, PageSize: 20, SortBy: "group", SortOrder: "asc"}, service.APIKeyListFilters{Scope: tc.scope, GroupID: tc.groupID})
			require.NoError(t, err)
			require.EqualValues(t, len(tc.want), total.Total)
			ids := make([]int64, 0, len(got))
			for _, k := range got {
				ids = append(ids, k.ID)
				if k.IsComposite || k.SmartRouting {
					require.Len(t, k.CompositeGroups, 2)
				}
			}
			require.Equal(t, tc.want, ids)
		})
	}
}
