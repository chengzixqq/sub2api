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

// WorkspaceGroupGrant 记录「站长把某个分组开放给某家供应商」。
//
// 这张表是多对多归属的唯一载体：groups 表刻意不加 workspace_id，
// 因为同一分组可同时授权给多家供应商，单值列无法表达。
//
// 用代理主键 id + UNIQUE(workspace_id, group_id)，而非复合主键：
// 语义等价，但避开了 Ent 在 field.ID 复合主键上的代码生成缺陷。
//
// base_priority 由站长设定、供应商不可自选：锁定它才能实现「A 家优先、
// B 家兜底」而不被供应商互相抬价破坏。
//
// 结算倍率刻意不在这张表上。它就是 accounts.rate_multiplier —— 那一列本
// 就是上游成本倍率，也正是与站长结算的口径。倍率是「每账号一个值」，而
// 一个账号可同时落在多个已授权分组里，若每组各带一个上限，同一账号会撞上
// 多个冲突区间，无从判定用哪个；因此可调区间挂在 workspaces 上（一家供应商
// 一个约定价区间），见 workspace.go 的 settlement_rate_min/max。
type WorkspaceGroupGrant struct {
	ent.Schema
}

func (WorkspaceGroupGrant) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "workspace_group_grants"},
	}
}

func (WorkspaceGroupGrant) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("workspace_id"),
		field.Int64("group_id"),
		field.Int("base_priority").
			Default(50).
			Comment("该供应商在此分组新增账号时强制套用的 account_groups.priority。"),
		field.Bool("enabled").
			Default(true).
			Comment("关闭后该供应商立即失去对此分组的所有操作权。"),
		// 手写时间列而非用 TimeMixin：这张表没有软删除，
		// 与 account_group.go 的处理方式一致。
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (WorkspaceGroupGrant) Edges() []ent.Edge {
	return []ent.Edge{
		// 与 account_group.go 一致：两侧都用 edge.To，不在 Workspace/Group
		// 上声明反向 edge —— 这张表是纯关联表，反向遍历一律走显式查询。
		edge.To("workspace", Workspace.Type).
			Unique().
			Required().
			Field("workspace_id"),
		edge.To("group", Group.Type).
			Unique().
			Required().
			Field("group_id"),
	}
}

func (WorkspaceGroupGrant) Indexes() []ent.Index {
	return []ent.Index{
		// 同一分组对同一工作区只能有一条授权。
		index.Fields("workspace_id", "group_id").Unique(),
		index.Fields("group_id"),
	}
}
