package admin

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountHandlerListLiteUsesCompactDTOAndETag(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(77)
	adminSvc.accounts = []service.Account{{
		ID: 501, Name: "compact-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"email": "compact@example.com", "access_token": strings.Repeat("x", 4096)},
		Extra:       map[string]any{"privacy_mode": "training_off"}, Status: service.StatusActive,
		Schedulable: true, Concurrency: 4, GroupIDs: []int64{groupID},
		Groups:        []*service.Group{{ID: groupID, Name: "codex", Platform: service.PlatformOpenAI}},
		AccountGroups: []service.AccountGroup{{AccountID: 501, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "codex", Platform: service.PlatformOpenAI}}},
		CreatedAt:     now, UpdatedAt: now,
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Header().Get("ETag"))

	var litePayload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &litePayload))
	require.Len(t, litePayload.Data.Items, 1)
	liteItem := litePayload.Data.Items[0]
	require.Equal(t, float64(501), liteItem["id"])
	require.Equal(t, []any{float64(groupID)}, liteItem["group_ids"])
	require.Equal(t, true, liteItem["schedulable"])
	credentials, ok := liteItem["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "compact@example.com", credentials["email"])
	require.NotContains(t, credentials, "access_token")
	credentialsStatus, ok := liteItem["credentials_status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, credentialsStatus["has_access_token"])

	// ETag 必须代表同一份压缩响应，刷新请求应返回 304。
	rec304 := httptest.NewRecorder()
	req304 := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil)
	req304.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	router.ServeHTTP(rec304, req304)
	require.Equal(t, http.StatusNotModified, rec304.Code)

	// 省略 lite 时仍保留完整响应结构。
	recFull := httptest.NewRecorder()
	reqFull := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20", nil)
	router.ServeHTTP(recFull, reqFull)
	require.Equal(t, http.StatusOK, recFull.Code)
	var fullPayload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recFull.Body.Bytes(), &fullPayload))
	require.Contains(t, fullPayload.Data.Items[0], "groups")
	require.Contains(t, fullPayload.Data.Items[0], "account_groups")
}

func TestAccountHandlerListLiteStaysBelowResponseBudget(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	accounts := make([]service.Account, 20)
	for i := range accounts {
		id := int64(600 + i)
		groupID := int64(800 + i)
		accounts[i] = service.Account{
			ID: id, Name: "account-" + strconv.Itoa(i), Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
			Concurrency: 4, GroupIDs: []int64{groupID},
			Groups:        []*service.Group{{ID: groupID, Name: "group-" + strconv.Itoa(i), Description: strings.Repeat("description ", 20)}},
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "group-" + strconv.Itoa(i), Description: strings.Repeat("description ", 20)}}},
			CreatedAt:     now, UpdatedAt: now,
		}
	}
	adminSvc.accounts = accounts

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Less(t, rec.Body.Len(), 80*1024)

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, err := zw.Write(rec.Body.Bytes())
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, compressed.Len(), 15*1024)
}

func setupAccountListRouter() (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts", handler.List)
	return router, adminSvc
}

func TestAccountHandlerListIncludesCreatedAt(t *testing.T) {
	router, adminSvc := setupAccountListRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&sort_by=created_at&sort_order=desc", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", adminSvc.lastListAccounts.sortBy)

	var payload struct {
		Data struct {
			Items []struct {
				ID        int64  `json:"id"`
				CreatedAt string `json:"created_at"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)

	createdAt := payload.Data.Items[0].CreatedAt
	require.NotEmpty(t, createdAt)
	require.True(t, strings.HasSuffix(createdAt, "Z"), "created_at 应序列化为 UTC")
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	require.NoError(t, err)
	_, offset := parsed.Zone()
	require.Equal(t, 0, offset)
}

func TestAccountHandlerListReturnsSchedulerScoresPerGroup(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(41)
	adminSvc.accounts = []service.Account{
		{
			ID:          101,
			Name:        "account-high-priority",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			AccountGroups: []service.AccountGroup{
				{AccountID: 101, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "openai", Platform: service.PlatformOpenAI, SchedulerType: service.GroupSchedulerTypeAdvanced}},
			},
			GroupIDs:  []int64{groupID},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:          102,
			Name:        "account-low-priority",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    100000,
			AccountGroups: []service.AccountGroup{
				{AccountID: 102, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "openai", Platform: service.PlatformOpenAI, SchedulerType: service.GroupSchedulerTypeAdvanced}},
			},
			GroupIDs:  []int64{groupID},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				ID             int64 `json:"id"`
				SchedulerScore struct {
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_score"`
				SchedulerScores []struct {
					GroupID   *int64  `json:"group_id"`
					GroupName string  `json:"group_name"`
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 2)

	var high, low *struct {
		ID             int64 `json:"id"`
		SchedulerScore struct {
			BaseScore float64 `json:"base_score"`
		} `json:"scheduler_score"`
		SchedulerScores []struct {
			GroupID   *int64  `json:"group_id"`
			GroupName string  `json:"group_name"`
			BaseScore float64 `json:"base_score"`
		} `json:"scheduler_scores"`
	}
	for i := range payload.Data.Items {
		item := &payload.Data.Items[i]
		switch item.ID {
		case 101:
			high = item
		case 102:
			low = item
		}
	}
	require.NotNil(t, high)
	require.NotNil(t, low)
	require.Len(t, high.SchedulerScores, 1)
	require.Len(t, low.SchedulerScores, 1)
	require.Equal(t, groupID, *high.SchedulerScores[0].GroupID)
	require.Equal(t, "openai", high.SchedulerScores[0].GroupName)
	require.Greater(t, high.SchedulerScores[0].BaseScore, low.SchedulerScores[0].BaseScore)
}

func TestAccountHandlerListNonOpenAIStickyScoreExcludesPreviousResponse(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(45)
	stickyWeighted := true
	previousWeight := 11.0
	sessionWeight := 7.0
	group := &service.Group{
		ID:            groupID,
		Name:          "gemini",
		Platform:      service.PlatformGemini,
		SchedulerType: service.GroupSchedulerTypeAdvanced,
		AdvancedSchedulerOverrides: service.GroupAdvancedSchedulerOverrides{
			StickyWeightedEnabled:  &stickyWeighted,
			WeightPreviousResponse: &previousWeight,
			WeightSessionSticky:    &sessionWeight,
		},
	}
	adminSvc.accounts = []service.Account{
		{
			ID: 105, Name: "gemini-account", Platform: service.PlatformGemini,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
			Concurrency: 10, Priority: 1,
			AccountGroups: []service.AccountGroup{{AccountID: 105, GroupID: groupID, Group: group}},
			GroupIDs:      []int64{groupID}, CreatedAt: now, UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&platform=gemini&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				SchedulerScores []struct {
					BaseScore   float64 `json:"base_score"`
					StickyScore float64 `json:"sticky_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Len(t, payload.Data.Items[0].SchedulerScores, 1)
	score := payload.Data.Items[0].SchedulerScores[0]
	require.InDelta(t, score.BaseScore+sessionWeight, score.StickyScore, 0.000001)
}

func TestAccountHandlerListReusesHydratedGroupsWithoutRepositoryLookups(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	firstGroup := &service.Group{
		ID: 51, Name: "openai-primary", Platform: service.PlatformOpenAI,
		SchedulerType: service.GroupSchedulerTypeAdvanced,
	}
	secondGroup := &service.Group{
		ID: 52, Name: "openai-secondary", Platform: service.PlatformOpenAI,
		SchedulerType: service.GroupSchedulerTypeAdvanced,
	}
	accountGroups := func(accountID int64) []service.AccountGroup {
		return []service.AccountGroup{
			{AccountID: accountID, GroupID: firstGroup.ID, Group: firstGroup},
			{AccountID: accountID, GroupID: secondGroup.ID, Group: secondGroup},
		}
	}
	adminSvc.accounts = []service.Account{
		{
			ID: 111, Name: "first", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 10, Priority: 1,
			AccountGroups: accountGroups(111), GroupIDs: []int64{51, 52, 51}, CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: 112, Name: "second", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 10, Priority: 2,
			AccountGroups: accountGroups(112), GroupIDs: []int64{52, 51, 52}, CreatedAt: now, UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, adminSvc.getGroupCalls)
	require.Equal(t, []int64{firstGroup.ID, secondGroup.ID}, adminSvc.openAISchedulerScorePoolGroupIDs)
	require.Equal(t, 2, adminSvc.openAISchedulerScorePoolCalls)

	var payload struct {
		Data struct {
			Items []struct {
				SchedulerScores []struct {
					GroupID *int64 `json:"group_id"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 2)
	for _, item := range payload.Data.Items {
		require.Len(t, item.SchedulerScores, 2)
		groupIDs := []int64{*item.SchedulerScores[0].GroupID, *item.SchedulerScores[1].GroupID}
		require.ElementsMatch(t, []int64{firstGroup.ID, secondGroup.ID}, groupIDs)
	}
}

func TestAccountHandlerListSkipsSchedulerScoresByDefault(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	adminSvc.accounts = []service.Account{
		{
			ID:          110,
			Name:        "openai-account",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&platform=openai", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, adminSvc.schedulerScoreFilterCalls)
	require.Zero(t, adminSvc.openAISchedulerScorePoolCalls)

	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.NotContains(t, payload.Data.Items[0], "scheduler_score")
	require.NotContains(t, payload.Data.Items[0], "scheduler_scores")
}

func TestAccountHandlerListKeepsSchedulerScoreScopedToFilter(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(42)
	visibleAccount := service.Account{
		ID:          201,
		Name:        "visible-low-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    100000,
		AccountGroups: []service.AccountGroup{
			{AccountID: 201, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "openai", Platform: service.PlatformOpenAI, SchedulerType: service.GroupSchedulerTypeAdvanced}},
		},
		GroupIDs:  []int64{groupID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	hiddenGroupPeer := service.Account{
		ID:          202,
		Name:        "hidden-high-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    1,
		AccountGroups: []service.AccountGroup{
			{AccountID: 202, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "openai", Platform: service.PlatformOpenAI, SchedulerType: service.GroupSchedulerTypeAdvanced}},
		},
		GroupIDs:  []int64{groupID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	adminSvc.accounts = []service.Account{visibleAccount}
	adminSvc.accountSchedulerScoreFilterAccounts = []service.Account{visibleAccount, hiddenGroupPeer}
	adminSvc.openAISchedulerScorePoolAccounts = []service.Account{visibleAccount, hiddenGroupPeer}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=1&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				ID             int64 `json:"id"`
				SchedulerScore struct {
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_score"`
				SchedulerScores []struct {
					GroupID   *int64  `json:"group_id"`
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	item := payload.Data.Items[0]
	require.Equal(t, int64(201), item.ID)
	require.Len(t, item.SchedulerScores, 1)
	require.Equal(t, groupID, *item.SchedulerScores[0].GroupID)
	require.Equal(t, item.SchedulerScores[0].BaseScore, item.SchedulerScore.BaseScore)
}

func TestAccountHandlerListSchedulerScoreIgnoresPagination(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	visibleAccount := service.Account{
		ID:          301,
		Name:        "visible-low-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    100000,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	hiddenFilterPeer := service.Account{
		ID:          302,
		Name:        "hidden-high-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	adminSvc.accounts = []service.Account{visibleAccount}
	adminSvc.accountSchedulerScoreFilterAccounts = []service.Account{visibleAccount, hiddenFilterPeer}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=1&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				ID             int64 `json:"id"`
				SchedulerScore struct {
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_score"`
				SchedulerScores []struct {
					GroupID   *int64  `json:"group_id"`
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, int64(301), payload.Data.Items[0].ID)
	require.Less(t, payload.Data.Items[0].SchedulerScore.BaseScore, 3.75)
	require.Empty(t, payload.Data.Items[0].SchedulerScores)
}
