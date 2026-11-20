package websearch

import "context"

// Provider is the interface every search backend must implement.
type Provider interface {
	// Name 返回搜索供应商标识。
	Name() string
	// Search executes a web search and returns results.
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}
