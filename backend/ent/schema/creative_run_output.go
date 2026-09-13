package schema

import (
	"github.com/TokenFlux/TokenRouter/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CreativeRunOutput 保存创作台任务单个输出的元数据。
//
// 隐私红线：输出图片本体只保存在临时 Redis 存储（TTL 到期即失效），
// 这张表只记录状态、MIME、字节数与临时过期时间等描述信息。
type CreativeRunOutput struct {
	ent.Schema
}

func (CreativeRunOutput) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "creative_run_outputs"},
	}
}

func (CreativeRunOutput) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (CreativeRunOutput) Fields() []ent.Field {
	return []ent.Field{
		field.String("run_id").MaxLen(64),
		field.Int("output_index"),
		field.String("status").MaxLen(20).Default("pending"),
		field.String("mime_type").Optional().Nillable().MaxLen(128),
		field.Int64("byte_size").Optional().Nillable(),
		// transient_expires_at 记录临时输出的过期时间；过期后不得再提供给客户端。
		field.Time("transient_expires_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("acked_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("error_code").Optional().Nillable().MaxLen(128),
		field.String("error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
	}
}

func (CreativeRunOutput) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("run_id", "output_index").Unique(),
		index.Fields("run_id", "status"),
		index.Fields("transient_expires_at"),
	}
}
