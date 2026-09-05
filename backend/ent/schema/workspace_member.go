package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// WorkspaceMember 把供应商用户绑定到工作区。
//
// user_id 是主键（而非复合键）：一个用户至多属于一个工作区，
// 这让 VendorScope 中间件能用一次点查确定作用域。
type WorkspaceMember struct {
	ent.Schema
}

func (WorkspaceMember) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "workspace_members"},
	}
}

func (WorkspaceMember) Fields() []ent.Field {
	return []ent.Field{
		// user_id 唯一而非主键：一个用户至多属于一个工作区，
		// 这让 VendorScope 中间件能用一次点查确定作用域。
		field.Int64("user_id").
			Unique(),
		field.Int64("workspace_id"),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (WorkspaceMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Unique().
			Required().
			Field("user_id"),
		edge.From("workspace", Workspace.Type).
			Ref("members").
			Unique().
			Required().
			Field("workspace_id"),
	}
}

func (WorkspaceMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("workspace_id"),
	}
}
