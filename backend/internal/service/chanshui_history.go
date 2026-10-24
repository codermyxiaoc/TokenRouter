package service

import (
	"context"
	"sort"
)

// 批量能力保持为内部可选接口，既有仓储替身可继续使用单配置读取。
type chanshuiRecentRunsReader interface {
	ListRecentRuns(context.Context, []int64) (map[int64][]ChanshuiRun, error)
}

// populateRecentRuns 一次批量读取列表摘要，不因配置数量增长发出逐模型查询。
func (s *ChanshuiService) populateRecentRuns(ctx context.Context, configs []ChanshuiConfig) ([]ChanshuiConfig, error) {
	if len(configs) == 0 {
		return configs, nil
	}
	byConfig := make(map[int64][]ChanshuiRun, len(configs))
	if reader, ok := s.repo.(chanshuiRecentRunsReader); ok {
		ids := make([]int64, len(configs))
		for i := range configs {
			ids[i] = configs[i].ID
		}
		var err error
		byConfig, err = reader.ListRecentRuns(ctx, ids)
		if err != nil {
			return nil, err
		}
	} else {
		for _, config := range configs {
			runs, err := s.repo.ListRuns(ctx, config.ID)
			if err != nil {
				return nil, err
			}
			byConfig[config.ID] = runs
		}
	}
	for i := range configs {
		config := &configs[i]
		runs := make([]ChanshuiRun, 0, len(byConfig[config.ID]))
		for _, run := range byConfig[config.ID] {
			if run.ConfigID != config.ID {
				continue
			}
			// 列表只含轻量摘要，完整报告仍由单次详情接口按需读取。
			run.Report = nil
			runs = append(runs, run)
		}
		sort.Slice(runs, func(a, b int) bool {
			if runs[a].CreatedAt.Equal(runs[b].CreatedAt) {
				return runs[a].ID > runs[b].ID
			}
			return runs[a].CreatedAt.After(runs[b].CreatedAt)
		})
		if len(runs) > 60 {
			runs = runs[:60]
		}
		config.LatestRun = nil
		config.RecentRuns = runs
		if len(runs) > 0 {
			config.LatestRun = &config.RecentRuns[0]
		}
	}
	return configs, nil
}
