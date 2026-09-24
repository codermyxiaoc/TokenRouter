package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type mediaTaskRepoStub struct {
	observation MediaTaskObservation
	actor       MediaTaskActor
	filter      MediaTaskFilter
}

func (s *mediaTaskRepoStub) Observe(_ context.Context, o MediaTaskObservation) error {
	s.observation = o
	return nil
}
func (s *mediaTaskRepoStub) List(_ context.Context, a MediaTaskActor, f MediaTaskFilter) (*MediaTaskList, error) {
	s.actor, s.filter = a, f
	account := int64(42)
	return &MediaTaskList{Items: []MediaTask{{ID: 1, AccountID: &account, User: &MediaTaskUser{ID: 7, Email: "owner@example.test", Username: "任务用户"}}}}, nil
}
func (s *mediaTaskRepoStub) Get(_ context.Context, a MediaTaskActor, id int64) (*MediaTask, error) {
	s.actor = a
	account := int64(42)
	return &MediaTask{ID: id, AccountID: &account, User: &MediaTaskUser{ID: 7, Email: "owner@example.test", Username: "任务用户"}}, nil
}

func TestMediaTaskObservationRejectsInvalidAndRedactsSecrets(t *testing.T) {
	repo := &mediaTaskRepoStub{}
	s := NewMediaTaskService(repo)
	o := MediaTaskObservation{Source: "async_image", TaskID: "imgtask_test", MediaType: "image", UserID: 1, APIKeyID: 2, Status: "completed",
		ErrorMessage: "failed sk-abcdefgh1234567890 at https://upstream.test/image?signature=secret"}
	require.NoError(t, s.ObserveMediaTask(context.Background(), o))
	require.NotContains(t, repo.observation.ErrorMessage, "abcdefgh")
	require.NotContains(t, repo.observation.ErrorMessage, "upstream.test")
	require.False(t, repo.observation.CreatedAt.IsZero())
	require.NotNil(t, repo.observation.CompletedAt)
	o.MediaType = "video"
	require.ErrorIs(t, s.ObserveMediaTask(context.Background(), o), ErrMediaTaskInvalid)
	o.MediaType = "image"
	o.ErrorMessage = strings.Repeat("错", 600)
	require.NoError(t, s.ObserveMediaTask(context.Background(), o))
	require.Len(t, []rune(repo.observation.ErrorMessage), 500)
}

func TestMediaTaskListForcesOwnerAndHidesAccount(t *testing.T) {
	s := NewMediaTaskService(&mediaTaskRepoStub{})
	actor := MediaTaskActor{UserID: 7}
	got, err := s.List(context.Background(), actor, MediaTaskFilter{UserID: 9, PageSize: 1000})
	require.NoError(t, err)
	require.Nil(t, got.Items[0].AccountID)
	require.Nil(t, got.Items[0].User)
	require.Nil(t, got.Items[0].ActualCost, "未关联结算不能伪装为零费用")
	repo := s.repo.(*mediaTaskRepoStub)
	require.EqualValues(t, 7, repo.filter.UserID)
	require.Equal(t, 100, repo.filter.PageSize)
	require.Equal(t, 1, repo.filter.Page)
	admin, err := s.Get(context.Background(), MediaTaskActor{UserID: 1, IsAdmin: true}, 1)
	require.NoError(t, err)
	require.NotNil(t, admin.AccountID)
	require.Equal(t, "owner@example.test", admin.User.Email)
	_, err = s.List(context.Background(), actor, MediaTaskFilter{Status: "unknown"})
	require.ErrorIs(t, err, ErrMediaTaskInvalid)
	_, err = s.Get(context.Background(), MediaTaskActor{}, 1)
	require.ErrorIs(t, err, ErrMediaTaskNotFound)
}

func TestMediaTaskUserIdentityOnlyExposedToAdmin(t *testing.T) {
	s := NewMediaTaskService(&mediaTaskRepoStub{})
	ctx := context.Background()
	for _, admin := range []bool{false, true} {
		actor := MediaTaskActor{UserID: 7, IsAdmin: admin}
		list, err := s.List(ctx, actor, MediaTaskFilter{})
		require.NoError(t, err)
		detail, err := s.Get(ctx, actor, 1)
		require.NoError(t, err)
		for _, record := range []*MediaTask{&list.Items[0], detail} {
			encoded, err := json.Marshal(record)
			require.NoError(t, err)
			if admin {
				require.NotNil(t, record.User)
				require.Equal(t, "任务用户", record.User.Username)
				require.Contains(t, string(encoded), `"user":{"id":7,"email":"owner@example.test","username":"任务用户"}`)
			} else {
				require.Nil(t, record.User)
				require.NotContains(t, string(encoded), `"user":`)
				require.NotContains(t, string(encoded), "owner@example.test")
			}
		}
	}
}

func TestMediaTaskObservationPreservesOriginalTaskTime(t *testing.T) {
	repo := &mediaTaskRepoStub{}
	s := NewMediaTaskService(repo)
	created := time.Now().Add(-time.Minute)
	require.NoError(t, s.ObserveMediaTask(context.Background(), MediaTaskObservation{Source: "seedance_video", TaskID: "task1", MediaType: "video", UserID: 1, APIKeyID: 2, Status: "processing", CreatedAt: created}))
	require.Equal(t, created, repo.observation.CreatedAt)
	require.Nil(t, repo.observation.CompletedAt)
}
