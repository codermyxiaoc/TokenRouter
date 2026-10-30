//go:build integration

package repository

import (
	"context"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TokenFlux/TokenRouter/internal/service"
	dbmigrations "github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func apiDocText(v string) *string { return &v }

func cleanAPIDocs(t *testing.T) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM api_docs; DELETE FROM api_doc_categories`)
	require.NoError(t, err)
}

// 文档叠加的增删改查：新建冲突、版本冲突、内置恢复默认保留排序、自定义删除。
func TestAPIDocRepositoryDocumentLifecycle(t *testing.T) {
	cleanAPIDocs(t)
	t.Cleanup(func() { cleanAPIDocs(t) })
	ctx := context.Background()
	repo := NewAPIDocRepository(integrationDB)
	svc := service.NewAPIDocService(repo)

	// 自定义文档：必须完整；创建后同 ID 再次新建冲突。
	_, err := svc.SaveDocument(ctx, "my-guide", service.APIDocEntryInput{Custom: true, Title: apiDocText("指南")}, 1)
	require.Error(t, err)
	created, err := svc.SaveDocument(ctx, "my-guide", service.APIDocEntryInput{Custom: true, Title: apiDocText(" 指南 "),
		CategoryID: apiDocText("text"), Markdown: apiDocText("# 指南")}, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Version)
	require.Equal(t, "指南", *created.Title)
	_, err = svc.SaveDocument(ctx, "my-guide", service.APIDocEntryInput{Custom: true, Title: apiDocText("x"), CategoryID: apiDocText("text"), Markdown: apiDocText("y")}, 1)
	require.ErrorIs(t, err, service.ErrAPIDocConflict)

	// 改名：旧版本号提交冲突，当前版本成功并递增。
	renamed, err := svc.SaveDocument(ctx, "my-guide", service.APIDocEntryInput{Custom: true, Version: created.Version, Title: apiDocText("新指南"), CategoryID: apiDocText("text"), Markdown: apiDocText("# 新")}, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), renamed.Version)
	_, err = svc.SaveDocument(ctx, "my-guide", service.APIDocEntryInput{Custom: true, Version: 1, Title: apiDocText("覆盖"), CategoryID: apiDocText("text"), Markdown: apiDocText("z")}, 1)
	require.ErrorIs(t, err, service.ErrAPIDocConflict)

	// 内置文档先排序（占位行不可见），再改正文接管占位行并保留排序。
	require.NoError(t, svc.Reorder(ctx, service.APIDocOrderInput{Documents: map[string][]string{"text": {"messages", "my-guide"}}}, 1))
	builtin, err := svc.SaveDocument(ctx, "messages", service.APIDocEntryInput{Markdown: apiDocText("# 改写")}, 1)
	require.NoError(t, err)
	require.Equal(t, int64(1), builtin.Version)
	require.NotNil(t, builtin.Sort)
	require.Equal(t, 10, *builtin.Sort)

	// 隐藏后公开目录与公开单篇都看不到。
	hidden, err := svc.SaveDocument(ctx, "messages", service.APIDocEntryInput{Markdown: apiDocText("# 改写"), Hidden: true, Version: builtin.Version}, 1)
	require.NoError(t, err)
	// 公开目录保留隐藏标记（前端据此过滤内置条目），但绝不携带正文。
	public, err := svc.Catalog(ctx, true)
	require.NoError(t, err)
	var hiddenMarked bool
	for _, doc := range public.Documents {
		require.Nil(t, doc.Markdown, "目录不携带正文")
		if doc.ID == "messages" {
			require.True(t, doc.Hidden, "隐藏标记必须保留，否则内置文档会重新显示")
			hiddenMarked = true
		}
	}
	require.True(t, hiddenMarked)
	_, err = svc.Document(ctx, "messages", true)
	require.ErrorIs(t, err, service.ErrAPIDocNotFound)

	// 内置恢复默认：清除内容但保留排序；自定义删除后彻底消失。
	require.ErrorIs(t, svc.DeleteDocument(ctx, "messages", hidden.Version-1), service.ErrAPIDocConflict)
	require.NoError(t, svc.DeleteDocument(ctx, "messages", hidden.Version))
	all, err := svc.Catalog(ctx, false)
	require.NoError(t, err)
	var reset *service.APIDocEntry
	for i := range all.Documents {
		if all.Documents[i].ID == "messages" {
			reset = &all.Documents[i]
		}
	}
	require.NotNil(t, reset)
	require.False(t, reset.HasMarkdown)
	require.False(t, reset.Hidden)
	require.Equal(t, int64(0), reset.Version)
	require.Equal(t, 10, *reset.Sort)
	require.NoError(t, svc.DeleteDocument(ctx, "my-guide", renamed.Version))
	_, err = svc.Document(ctx, "my-guide", false)
	require.ErrorIs(t, err, service.ErrAPIDocNotFound)
}

// 分类：自定义分类非空不能删除，内置分类恢复默认保留排序。
func TestAPIDocRepositoryCategoryLifecycle(t *testing.T) {
	cleanAPIDocs(t)
	t.Cleanup(func() { cleanAPIDocs(t) })
	ctx := context.Background()
	svc := service.NewAPIDocService(NewAPIDocRepository(integrationDB))

	_, err := svc.SaveCategory(ctx, "faq", service.APIDocCategoryInput{Custom: true}, 1)
	require.Error(t, err, "自定义分类必须有标题")
	faq, err := svc.SaveCategory(ctx, "faq", service.APIDocCategoryInput{Custom: true, Title: apiDocText("常见问题")}, 1)
	require.NoError(t, err)
	doc, err := svc.SaveDocument(ctx, "faq-billing", service.APIDocEntryInput{Custom: true, Title: apiDocText("计费"), CategoryID: apiDocText("faq"), Markdown: apiDocText("# 计费")}, 1)
	require.NoError(t, err)
	require.ErrorIs(t, svc.DeleteCategory(ctx, "faq", faq.Version), service.ErrAPIDocCategoryNotEmpty)
	require.NoError(t, svc.DeleteDocument(ctx, "faq-billing", doc.Version))
	require.NoError(t, svc.DeleteCategory(ctx, "faq", faq.Version))

	require.NoError(t, svc.Reorder(ctx, service.APIDocOrderInput{Categories: []string{"video", "text"}}, 1))
	video, err := svc.SaveCategory(ctx, "video", service.APIDocCategoryInput{Title: apiDocText("视频"), Hidden: true}, 1)
	require.NoError(t, err)
	public, err := svc.Catalog(ctx, true)
	require.NoError(t, err)
	var hiddenCategoryMarked bool
	for _, category := range public.Categories {
		if category.ID == "video" {
			require.True(t, category.Hidden, "隐藏标记必须保留，否则内置分类会重新显示")
			hiddenCategoryMarked = true
		}
	}
	require.True(t, hiddenCategoryMarked)
	require.NoError(t, svc.DeleteCategory(ctx, "video", video.Version))
	all, err := svc.Catalog(ctx, false)
	require.NoError(t, err)
	for _, category := range all.Categories {
		if category.ID == "video" {
			require.Nil(t, category.Title)
			require.False(t, category.Hidden)
			require.Equal(t, 10, *category.Sort)
		}
	}
	require.Error(t, svc.Reorder(ctx, service.APIDocOrderInput{Categories: []string{"text", "text"}}, 1))
}

// 隐藏分类后的文档：公开目录保留隐藏标记供前端过滤，单篇接口必须对访客返回 404。
func TestAPIDocHiddenCategoryBlocksPublicDocument(t *testing.T) {
	cleanAPIDocs(t)
	t.Cleanup(func() { cleanAPIDocs(t) })
	ctx := context.Background()
	svc := service.NewAPIDocService(NewAPIDocRepository(integrationDB))
	category, err := svc.SaveCategory(ctx, "draft", service.APIDocCategoryInput{Custom: true, Title: apiDocText("草稿")}, 1)
	require.NoError(t, err)
	doc, err := svc.SaveDocument(ctx, "draft-doc", service.APIDocEntryInput{Custom: true, Title: apiDocText("草稿文档"), CategoryID: apiDocText("draft"), Markdown: apiDocText("# 未公开")}, 1)
	require.NoError(t, err)
	require.NoError(t, svc.DeleteDocument(ctx, "draft-doc", doc.Version))
	require.NoError(t, svc.DeleteCategory(ctx, "draft", category.Version))

	// 重建并直接隐藏分类，文档本身未标记隐藏。
	category, err = svc.SaveCategory(ctx, "draft", service.APIDocCategoryInput{Custom: true, Title: apiDocText("草稿")}, 1)
	require.NoError(t, err)
	doc, err = svc.SaveDocument(ctx, "draft-doc", service.APIDocEntryInput{Custom: true, Title: apiDocText("草稿文档"), CategoryID: apiDocText("draft"), Markdown: apiDocText("# 未公开")}, 1)
	require.NoError(t, err)
	hiddenCategory, err := svc.SaveCategory(ctx, "draft", service.APIDocCategoryInput{Custom: true, Title: apiDocText("草稿"), Hidden: true, Version: category.Version}, 1)
	require.NoError(t, err)
	_ = hiddenCategory

	// 公开目录仍保留隐藏分类的标记（前端据此过滤内置与自定义条目）。
	public, err := svc.Catalog(ctx, true)
	require.NoError(t, err)
	var marked bool
	for _, item := range public.Categories {
		if item.ID == "draft" {
			require.True(t, item.Hidden)
			marked = true
		}
	}
	require.True(t, marked, "公开目录必须保留隐藏标记，否则内置条目会重新显示")
	// 单篇接口不能靠已知地址读到隐藏分类下的内容。
	_, err = svc.Document(ctx, "draft-doc", true)
	require.ErrorIs(t, err, service.ErrAPIDocNotFound)
	// 管理端仍可读取。
	_, err = svc.Document(ctx, "draft-doc", false)
	require.NoError(t, err)
	// 文档自身隐藏同样拒绝。
	_, err = svc.SaveDocument(ctx, "draft-doc", service.APIDocEntryInput{Custom: true, Title: apiDocText("草稿文档"), CategoryID: apiDocText("draft"), Markdown: apiDocText("# 未公开"), Hidden: true, Version: doc.Version}, 1)
	require.NoError(t, err)
	_, err = svc.Document(ctx, "draft-doc", true)
	require.ErrorIs(t, err, service.ErrAPIDocNotFound)
	// 跨分类重复排序必须拒绝。
	require.Error(t, svc.Reorder(ctx, service.APIDocOrderInput{Documents: map[string][]string{"draft": {"draft-doc"}, "text": {"draft-doc"}}}, 1))
}

// 两级目录：二级分类挂在一级分类下，最多两级；有子分类或文档的自定义分类不能被彻底删除。
func TestAPIDocTwoLevelCategories(t *testing.T) {
	cleanAPIDocs(t)
	t.Cleanup(func() { cleanAPIDocs(t) })
	ctx := context.Background()
	svc := service.NewAPIDocService(NewAPIDocRepository(integrationDB))

	parent, err := svc.SaveCategory(ctx, "sdk", service.APIDocCategoryInput{Custom: true, Title: apiDocText("SDK")}, 1)
	require.NoError(t, err)
	require.Nil(t, parent.ParentID)
	child, err := svc.SaveCategory(ctx, "sdk-python", service.APIDocCategoryInput{Custom: true, Title: apiDocText("Python"), ParentID: apiDocText("sdk")}, 1)
	require.NoError(t, err)
	require.Equal(t, "sdk", *child.ParentID)

	// 不能挂到二级分类下（超过两级），不能以自身为上级，已有子分类的分类不能再降级。
	_, err = svc.SaveCategory(ctx, "sdk-python-v3", service.APIDocCategoryInput{Custom: true, Title: apiDocText("v3"), ParentID: apiDocText("sdk-python")}, 1)
	require.Error(t, err)
	_, err = svc.SaveCategory(ctx, "loop", service.APIDocCategoryInput{Custom: true, Title: apiDocText("x"), ParentID: apiDocText("loop")}, 1)
	require.Error(t, err)
	other, err := svc.SaveCategory(ctx, "other", service.APIDocCategoryInput{Custom: true, Title: apiDocText("Other")}, 1)
	require.NoError(t, err)
	_, err = svc.SaveCategory(ctx, "sdk", service.APIDocCategoryInput{Custom: true, Title: apiDocText("SDK"), ParentID: apiDocText("other"), Version: parent.Version}, 1)
	require.Error(t, err, "已有子分类的分类不能变成二级分类")

	// 有子分类时彻底删除被拒绝；先删子分类后可删除。
	require.ErrorIs(t, svc.DeleteCategory(ctx, "sdk", parent.Version), service.ErrAPIDocCategoryNotEmpty)
	require.NoError(t, svc.DeleteCategory(ctx, "sdk-python", child.Version))
	require.NoError(t, svc.DeleteCategory(ctx, "sdk", parent.Version))
	require.NoError(t, svc.DeleteCategory(ctx, "other", other.Version))

	// 二级分类的上级被隐藏时，其下文档对访客的直达读取同样返回 404。
	parent, err = svc.SaveCategory(ctx, "sdk", service.APIDocCategoryInput{Custom: true, Title: apiDocText("SDK")}, 1)
	require.NoError(t, err)
	_, err = svc.SaveCategory(ctx, "sdk-go", service.APIDocCategoryInput{Custom: true, Title: apiDocText("Go"), ParentID: apiDocText("sdk")}, 1)
	require.NoError(t, err)
	_, err = svc.SaveDocument(ctx, "sdk-go-intro", service.APIDocEntryInput{Custom: true, Title: apiDocText("入门"), CategoryID: apiDocText("sdk-go"), Markdown: apiDocText("# Go")}, 1)
	require.NoError(t, err)
	_, err = svc.Document(ctx, "sdk-go-intro", true)
	require.NoError(t, err)
	_, err = svc.SaveCategory(ctx, "sdk", service.APIDocCategoryInput{Custom: true, Title: apiDocText("SDK"), Hidden: true, Version: parent.Version}, 1)
	require.NoError(t, err)
	_, err = svc.Document(ctx, "sdk-go-intro", true)
	require.ErrorIs(t, err, service.ErrAPIDocNotFound)

	// 内置二级分类（如 video-openai）可改名与隐藏，上级保持内置。
	builtin, err := svc.SaveCategory(ctx, "video-openai", service.APIDocCategoryInput{Title: apiDocText("OpenAI 视频"), Hidden: true}, 1)
	require.NoError(t, err)
	require.Nil(t, builtin.ParentID)
	require.True(t, builtin.Hidden)

	// 上级必须真实存在且是一级分类：不存在的上级、内置二级分类作上级（第三级）都拒绝。
	_, err = svc.SaveCategory(ctx, "ghost-child", service.APIDocCategoryInput{Custom: true, Title: apiDocText("x"), ParentID: apiDocText("no-such-parent")}, 1)
	require.Error(t, err)
	_, err = svc.SaveCategory(ctx, "third-level", service.APIDocCategoryInput{Custom: true, Title: apiDocText("x"), ParentID: apiDocText("video-grok")}, 1)
	require.Error(t, err)
	// 内置一级分类可作上级；内置分类的层级不能改挂。
	_, err = svc.SaveCategory(ctx, "video-sora", service.APIDocCategoryInput{Custom: true, Title: apiDocText("Sora"), ParentID: apiDocText("video")}, 1)
	require.NoError(t, err)
	_, err = svc.SaveCategory(ctx, "video-grok", service.APIDocCategoryInput{ParentID: apiDocText("text")}, 1)
	require.Error(t, err)

	// 只隐藏内置一级分类“视频”时，没有叠加行的内置二级分类下的文档对访客同样不可直读。
	_, err = svc.SaveDocument(ctx, "grok-guide", service.APIDocEntryInput{Custom: true, Title: apiDocText("Grok"), CategoryID: apiDocText("video-grok"), Markdown: apiDocText("# g")}, 1)
	require.NoError(t, err)
	_, err = svc.Document(ctx, "grok-guide", true)
	require.NoError(t, err)
	_, err = svc.SaveCategory(ctx, "video", service.APIDocCategoryInput{Hidden: true}, 1)
	require.NoError(t, err)
	_, err = svc.Document(ctx, "grok-guide", true)
	require.ErrorIs(t, err, service.ErrAPIDocNotFound)
}

// 升级迁移：旧设置中的逐篇改写转为文档叠加行，损坏的旧值不阻断升级。
func TestAPIDocMigrationImportsLegacyOverrides(t *testing.T) {
	for name, legacy := range map[string]string{
		"valid":   `{"quickstart":"# 自定义快速开始","BAD ID":"x","errors":"   "}`,
		"corrupt": `not-json`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			container, err := tcpostgres.Run(ctx, selectDockerImage(ctx, postgresImageTag), tcpostgres.WithDatabase("api_docs_upgrade"),
				tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"), tcpostgres.BasicWaitStrategies())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
			dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
			require.NoError(t, err)
			db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			names, err := fs.Glob(dbmigrations.FS, "*.sql")
			require.NoError(t, err)
			history := fstest.MapFS{}
			for _, file := range names {
				if file >= "323_" {
					continue
				}
				body, err := dbmigrations.FS.ReadFile(file)
				require.NoError(t, err)
				history[file] = &fstest.MapFile{Data: body}
			}
			require.NoError(t, applyMigrationsFS(ctx, db, history))
			_, err = db.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('api_doc_overrides',$1)`, legacy)
			require.NoError(t, err)
			require.NoError(t, ApplyMigrations(ctx, db))
			require.NoError(t, ApplyMigrations(ctx, db), "重复启动幂等")
			var count int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM api_docs`).Scan(&count))
			if name == "corrupt" {
				require.Zero(t, count)
				return
			}
			require.Equal(t, 1, count, "非法 ID 与空正文被跳过")
			doc, err := NewAPIDocRepository(db).GetDocument(ctx, "quickstart")
			require.NoError(t, err)
			require.Equal(t, "# 自定义快速开始", *doc.Markdown)
			require.False(t, doc.Custom)
			require.Equal(t, int64(1), doc.Version)
		})
	}
}

// 两个一级分类并发互设上级：层级锁内复核后最多只有一个成功，不能形成环或第三级。
func TestAPIDocConcurrentMutualParentsCannotFormCycle(t *testing.T) {
	cleanAPIDocs(t)
	t.Cleanup(func() { cleanAPIDocs(t) })
	ctx := context.Background()
	svc := service.NewAPIDocService(NewAPIDocRepository(integrationDB))
	for round := 0; round < 10; round++ {
		cleanAPIDocs(t)
		a, err := svc.SaveCategory(ctx, "cycle-a", service.APIDocCategoryInput{Custom: true, Title: apiDocText("A")}, 1)
		require.NoError(t, err)
		b, err := svc.SaveCategory(ctx, "cycle-b", service.APIDocCategoryInput{Custom: true, Title: apiDocText("B")}, 1)
		require.NoError(t, err)
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, pair := range [][2]string{{"cycle-a", "cycle-b"}, {"cycle-b", "cycle-a"}} {
			version := a.Version
			if pair[0] == "cycle-b" {
				version = b.Version
			}
			wg.Add(1)
			go func(i int, id, parent string, version int64) {
				defer wg.Done()
				<-start
				_, errs[i] = svc.SaveCategory(ctx, id, service.APIDocCategoryInput{Custom: true, Title: apiDocText(id), ParentID: apiDocText(parent), Version: version}, 1)
			}(i, pair[0], pair[1], version)
		}
		close(start)
		wg.Wait()
		require.False(t, errs[0] == nil && errs[1] == nil, "round %d: 两个互设上级的请求不能同时成功", round)
		var cycles int
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_doc_categories c JOIN api_doc_categories p ON c.parent_id=p.id WHERE p.parent_id IS NOT NULL`).Scan(&cycles))
		require.Zero(t, cycles, "round %d: 不能出现上级本身也有上级的层级", round)
	}
}
