package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Workspace holds the schema definition for the Workspace entity.
//
// 一个工作区代表一家供应商（或站长直管）。工作区是纯管理面概念：
// 它决定「谁能在后台看到/操作哪些账号与代理」，不参与网关调度决策。
//
// id=1 是迁移预置的「站长直管」工作区，承载所有未划归供应商的存量资源。
type Workspace struct {
	ent.Schema
}

func (Workspace) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "workspaces"},
	}
}

func (Workspace) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (Workspace) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			MaxLen(100).
			NotEmpty(),
		field.String("description").
			MaxLen(500).
			Default(""),
		field.String("status").
			MaxLen(20).
			Default("active").
			Comment("active | disabled；disabled 时该工作区成员无法登录管理端。"),

		// 五个权限档，默认全关：新建工作区在站长显式开权限前什么都做不了。
		field.Bool("perm_account_manage").
			Default(false).
			Comment("可增删改自己工作区的上游账号（含凭证）。"),
		field.Bool("perm_group_ops").
			Default(false).
			Comment("可改被授权分组的运维类字段（非计费）。"),
		field.Bool("perm_group_billing").
			Default(false).
			Comment("可改被授权分组的计费类字段；分组被多家共享时自动失效。"),
		field.Bool("perm_proxy_manage").
			Default(false).
			Comment("可增删改自己工作区的代理。"),
		field.Bool("perm_monitor_view").
			Default(false).
			Comment("可查看自己工作区账号的监控与统计（只读）。"),

		// 结算倍率区间：供应商在账号管理里改的就是 accounts.rate_multiplier，
		// 那一列本就是上游成本倍率，也正是与站长结算的口径，无需另立字段。
		//
		// 区间挂在工作区而非分组授权上：账号倍率是「每账号一个值」，而一个
		// 账号可同时落在多个已授权分组里，各组若各带上限，同一账号会撞上
		// 多个互相冲突的区间，无从判定该用哪个。与站长的结算价是「一家一个
		// 约定」，挂工作区正好一一对应。
		//
		// decimal(10,4) 与 accounts.rate_multiplier 同精度，避免边界值因
		// 精度差被误判越界（如 0.05 存成 0.050001 就会卡在 max=0.05 上）。
		field.Float("settlement_rate_min").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Optional().Nillable().
			Comment("供应商可自设的账号倍率下限；NULL 表示不限下限。"),
		field.Float("settlement_rate_max").
			SchemaType(map[string]string{dialect.Postgres: "decimal(10,4)"}).
			Optional().Nillable().
			Comment("供应商可自设的账号倍率上限；NULL 表示不允许供应商自改倍率。"),
	}
}

func (Workspace) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("members", WorkspaceMember.Type),
	}
}

func (Workspace) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("deleted_at"),
	}
}
