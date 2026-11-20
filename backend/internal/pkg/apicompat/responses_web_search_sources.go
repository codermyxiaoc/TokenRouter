package apicompat

import (
	"encoding/json"
	"slices"
)

const webSearchSourcesInclude = "web_search_call.action.sources"

// 为托管搜索请求所访问的网页来源。
func IncludeWebSearchSources(req *ResponsesRequest) {
	for _, tool := range req.Tools {
		if tool.Type == "web_search" {
			if !slices.Contains(req.Include, webSearchSourcesInclude) {
				req.Include = append(req.Include, webSearchSourcesInclude)
			}
			return
		}
	}
}

type webSearchResult struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title"`
}

// 按本次搜索收集结果，没有来源时保留兼容占位内容。
func webSearchCallResults(item *ResponsesOutput) json.RawMessage {
	results := []webSearchResult{}
	if action := item.Action; action != nil {
		var sources []json.RawMessage
		if json.Unmarshal(action.Sources, &sources) == nil && sources != nil {
			for _, raw := range sources {
				var source map[string]any
				_ = json.Unmarshal(raw, &source)
				if rawURL, ok := source["url"].(string); ok && rawURL != "" {
					results = append(results, newWebSearchResult(rawURL))
				}
			}
		} else if action.Type == "open_page" {
			var pageURL string
			if json.Unmarshal(action.URL, &pageURL) == nil && pageURL != "" {
				results = append(results, newWebSearchResult(pageURL))
			}
		}
	}
	out, _ := json.Marshal(results)
	return out
}

// Responses 来源缺少标题时使用 URL。
func newWebSearchResult(rawURL string) webSearchResult {
	return webSearchResult{Type: "web_search_result", URL: rawURL, Title: rawURL}
}
