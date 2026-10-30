package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/TokenFlux/TokenRouter/internal/service"
)

// apiDocRepository 用原生 SQL 保存 API 文档目录叠加；version 做乐观并发控制。
type apiDocRepository struct{ db *sql.DB }

func NewAPIDocRepository(db *sql.DB) service.APIDocRepository {
	return &apiDocRepository{db: db}
}

const apiDocCategoryColumns = `id,parent_id,title,sort,hidden,custom,version,updated_at`

// 目录不读取正文，只返回是否存在改写正文，避免公开接口一次性返回全部长文。
const apiDocEntrySummaryColumns = `id,category_id,title,NULL::text,markdown IS NOT NULL,sort,hidden,custom,version,updated_at`
const apiDocEntryColumns = `id,category_id,title,markdown,markdown IS NOT NULL,sort,hidden,custom,version,updated_at`

func scanAPIDocCategory(row rowScanner) (*service.APIDocCategory, error) {
	var c service.APIDocCategory
	var parent, title sql.NullString
	var sort sql.NullInt64
	if err := row.Scan(&c.ID, &parent, &title, &sort, &c.Hidden, &c.Custom, &c.Version, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.ParentID, c.Title = nullStringPtr(parent), nullStringPtr(title)
	c.Sort = nullIntPtr(sort)
	return &c, nil
}

func scanAPIDocEntry(row rowScanner) (*service.APIDocEntry, error) {
	var d service.APIDocEntry
	var category, title, markdown sql.NullString
	var sort sql.NullInt64
	if err := row.Scan(&d.ID, &category, &title, &markdown, &d.HasMarkdown, &sort, &d.Hidden, &d.Custom, &d.Version, &d.UpdatedAt); err != nil {
		return nil, err
	}
	d.CategoryID, d.Title, d.Markdown = nullStringPtr(category), nullStringPtr(title), nullStringPtr(markdown)
	d.Sort = nullIntPtr(sort)
	return &d, nil
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullIntPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

// nullableActor 把 0 视为未知操作者，写入 NULL。
func nullableActor(actorID int64) any {
	if actorID <= 0 {
		return nil
	}
	return actorID
}

func (r *apiDocRepository) Catalog(ctx context.Context) (*service.APIDocCatalog, error) {
	catalog := &service.APIDocCatalog{Categories: []service.APIDocCategory{}, Documents: []service.APIDocEntry{}}
	rows, err := r.db.QueryContext(ctx, `SELECT `+apiDocCategoryColumns+` FROM api_doc_categories ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		c, err := scanAPIDocCategory(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		catalog.Categories = append(catalog.Categories, *c)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	rows, err = r.db.QueryContext(ctx, `SELECT `+apiDocEntrySummaryColumns+` FROM api_docs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		d, err := scanAPIDocEntry(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		catalog.Documents = append(catalog.Documents, *d)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return catalog, nil
}

// closeRows 合并遍历错误与关闭错误。
func closeRows(rows *sql.Rows) error {
	iterErr := rows.Err()
	closeErr := rows.Close()
	if iterErr != nil {
		return iterErr
	}
	return closeErr
}

func (r *apiDocRepository) GetDocument(ctx context.Context, id string) (*service.APIDocEntry, error) {
	doc, err := scanAPIDocEntry(r.db.QueryRowContext(ctx, `SELECT `+apiDocEntryColumns+` FROM api_docs WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAPIDocNotFound
	}
	return doc, err
}

// SaveCategory：version=0 时插入（已存在即冲突），否则仅在 version 一致时更新并递增。
func (r *apiDocRepository) SaveCategory(ctx context.Context, id string, in service.APIDocCategoryInput, actorID int64) (*service.APIDocCategory, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// 层级变更按全局咨询锁串行：服务层的父级校验读的是保存前的目录，两个一级分类并发互设上级时各自都会通过，
	// 这里在锁内按最新数据复核，防止形成环或超过两级。
	if err := lockAPIDocCategoryHierarchy(ctx, tx); err != nil {
		return nil, err
	}
	if in.ParentID != nil {
		var invalid bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_doc_categories WHERE id=$1 AND parent_id IS NOT NULL)
 OR EXISTS(SELECT 1 FROM api_doc_categories WHERE parent_id=$2)`, *in.ParentID, id).Scan(&invalid); err != nil {
			return nil, err
		}
		if invalid {
			return nil, service.ErrAPIDocCategoryHierarchy
		}
	}
	var row *sql.Row
	if in.Version == 0 {
		// 排序占位行（version=0）可被接管并保留排序，其它已存在的行视为冲突。
		row = tx.QueryRowContext(ctx, `INSERT INTO api_doc_categories (id,parent_id,title,hidden,custom,updated_by)
 VALUES ($1,$2,$3,$4,$5,$6)
 ON CONFLICT (id) DO UPDATE SET parent_id=EXCLUDED.parent_id,title=EXCLUDED.title,hidden=EXCLUDED.hidden,custom=EXCLUDED.custom,
 updated_by=EXCLUDED.updated_by,version=1,updated_at=NOW() WHERE api_doc_categories.version=0
 RETURNING `+apiDocCategoryColumns,
			id, in.ParentID, in.Title, in.Hidden, in.Custom, nullableActor(actorID))
	} else {
		row = tx.QueryRowContext(ctx, `UPDATE api_doc_categories SET parent_id=$2,title=$3,hidden=$4,custom=$5,updated_by=$6,
 version=version+1,updated_at=NOW() WHERE id=$1 AND version=$7 RETURNING `+apiDocCategoryColumns,
			id, in.ParentID, in.Title, in.Hidden, in.Custom, nullableActor(actorID), in.Version)
	}
	saved, err := scanAPIDocCategory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAPIDocConflict
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// lockAPIDocCategoryHierarchy 串行化分类层级的写入与删除，锁随事务释放。
func lockAPIDocCategoryHierarchy(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('api-doc-category-hierarchy',0))`)
	return err
}

// DeleteCategory 在同一事务内确认版本，自定义分类下仍有文档时拒绝删除，避免文档失去归属。
func (r *apiDocRepository) DeleteCategory(ctx context.Context, id string, version int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// 与保存共用层级锁，避免删除上级的同时有新的子分类挂上来。
	if err := lockAPIDocCategoryHierarchy(ctx, tx); err != nil {
		return err
	}
	var custom bool
	err = tx.QueryRowContext(ctx, `SELECT custom FROM api_doc_categories WHERE id=$1 AND version=$2 FOR UPDATE`, id, version).Scan(&custom)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrAPIDocConflict
	}
	if err != nil {
		return err
	}
	if custom {
		// 仍有文档或子分类挂在下面时拒绝删除，避免它们失去归属。
		var used bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM api_docs WHERE category_id=$1)
 OR EXISTS(SELECT 1 FROM api_doc_categories WHERE parent_id=$1)`, id).Scan(&used); err != nil {
			return err
		}
		if used {
			return service.ErrAPIDocCategoryNotEmpty
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM api_doc_categories WHERE id=$1`, id); err != nil {
			return err
		}
		return tx.Commit()
	}
	// 内置分类“恢复默认”只清除内容叠加，保留管理员调整过的排序。
	if _, err := tx.ExecContext(ctx, `UPDATE api_doc_categories SET parent_id=NULL,title=NULL,hidden=FALSE,version=0,updated_at=NOW() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveDocument：version=0 时插入（已存在即冲突，仅排序占位行除外），否则仅在 version 一致时更新。
// 排序接口可能已为内置文档建立只有 sort 的占位行（version=0 不可见），此时新建会接管该行并保留排序。
func (r *apiDocRepository) SaveDocument(ctx context.Context, id string, in service.APIDocEntryInput, actorID int64) (*service.APIDocEntry, error) {
	var row *sql.Row
	if in.Version == 0 {
		row = r.db.QueryRowContext(ctx, `INSERT INTO api_docs (id,category_id,title,markdown,hidden,custom,updated_by)
 VALUES ($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT (id) DO UPDATE SET category_id=EXCLUDED.category_id,title=EXCLUDED.title,markdown=EXCLUDED.markdown,
 hidden=EXCLUDED.hidden,custom=EXCLUDED.custom,updated_by=EXCLUDED.updated_by,version=1,updated_at=NOW()
 WHERE api_docs.version=0
 RETURNING `+apiDocEntryColumns, id, in.CategoryID, in.Title, in.Markdown, in.Hidden, in.Custom, nullableActor(actorID))
	} else {
		row = r.db.QueryRowContext(ctx, `UPDATE api_docs SET category_id=$2,title=$3,markdown=$4,hidden=$5,custom=$6,updated_by=$7,
 version=version+1,updated_at=NOW() WHERE id=$1 AND version=$8 RETURNING `+apiDocEntryColumns,
			id, in.CategoryID, in.Title, in.Markdown, in.Hidden, in.Custom, nullableActor(actorID), in.Version)
	}
	saved, err := scanAPIDocEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAPIDocConflict
	}
	return saved, err
}

// DeleteDocument 删除自定义文档；内置文档“恢复默认”只清除内容叠加，保留排序。
// 两条语句各自带 version 条件，任一命中即成功；都未命中说明已被他人修改或删除。
func (r *apiDocRepository) DeleteDocument(ctx context.Context, id string, version int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM api_docs WHERE id=$1 AND version=$2 AND custom`, id, version)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	res, err = r.db.ExecContext(ctx, `UPDATE api_docs SET category_id=NULL,title=NULL,markdown=NULL,hidden=FALSE,version=0,updated_at=NOW()
 WHERE id=$1 AND version=$2 AND NOT custom`, id, version)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return service.ErrAPIDocConflict
	}
	return err
}

// Reorder 在一个事务内写入排序；内置条目没有叠加行时建立 version=0 的排序占位行，不改变内容版本。
func (r *apiDocRepository) Reorder(ctx context.Context, in service.APIDocOrderInput, actorID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for index, id := range in.Categories {
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_doc_categories (id,sort,version,updated_by) VALUES ($1,$2,0,$3)
 ON CONFLICT (id) DO UPDATE SET sort=EXCLUDED.sort`, id, (index+1)*10, nullableActor(actorID)); err != nil {
			return err
		}
	}
	for _, ids := range in.Documents {
		for index, id := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO api_docs (id,sort,version,updated_by) VALUES ($1,$2,0,$3)
 ON CONFLICT (id) DO UPDATE SET sort=EXCLUDED.sort`, id, (index+1)*10, nullableActor(actorID)); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
