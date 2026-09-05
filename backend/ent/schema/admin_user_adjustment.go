package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// AdminUserAdjustment is the immutable ledger for owner-initiated balance and concurrency changes.
type AdminUserAdjustment struct {
	ent.Schema
}

func (AdminUserAdjustment) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "admin_user_adjustments"}}
}

func (AdminUserAdjustment) Fields() []ent.Field {
	numeric := map[string]string{dialect.Postgres: "decimal(20,8)"}
	return []ent.Field{
		field.UUID("action_id", uuid.UUID{}),
		field.String("kind").MaxLen(20),
		field.String("operation").MaxLen(20),
		field.String("requested_value").SchemaType(numeric).Optional().Nillable(),
		field.String("delta").SchemaType(numeric),
		field.String("before_value").SchemaType(numeric).Optional().Nillable(),
		field.String("after_value").SchemaType(numeric).Optional().Nillable(),
		field.Int64("user_id").Optional().Nillable(),
		field.String("user_email").MaxLen(255).Optional().Nillable(),
		field.String("user_name").MaxLen(100).Optional().Nillable(),
		field.Int64("operator_user_id").Optional().Nillable(),
		field.String("operator_email").MaxLen(255).Optional().Nillable(),
		field.String("operator_name").MaxLen(100).Optional().Nillable(),
		field.String("notes").SchemaType(map[string]string{dialect.Postgres: "text"}).Optional().Nillable(),
		field.String("client_ip").MaxLen(64).Optional().Nillable(),
		field.String("auth_method").MaxLen(32).Optional().Nillable(),
		field.String("request_id").MaxLen(128).Optional().Nillable(),
		field.String("source").MaxLen(64),
		field.Int64("legacy_redeem_code_id").Optional().Nillable(),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (AdminUserAdjustment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("created_at", "id").
			StorageKey("idx_admin_user_adjustments_created").
			Annotations(entsql.DescColumns("created_at", "id")),
		index.Fields("user_id", "created_at", "id").
			StorageKey("idx_admin_user_adjustments_user_created").
			Annotations(entsql.DescColumns("created_at", "id")),
		index.Fields("operator_user_id", "created_at", "id").
			StorageKey("idx_admin_user_adjustments_operator_created").
			Annotations(entsql.DescColumns("created_at", "id")),
		index.Fields("kind", "created_at", "id").
			StorageKey("idx_admin_user_adjustments_kind_created").
			Annotations(entsql.DescColumns("created_at", "id")),
		index.Fields("action_id").
			StorageKey("idx_admin_user_adjustments_action"),
		index.Fields("action_id", "user_id", "kind").
			Unique().
			StorageKey("idx_admin_user_adjustments_action_user_kind").
			Annotations(entsql.IndexWhere("user_id IS NOT NULL")),
		index.Fields("legacy_redeem_code_id").
			Unique().
			StorageKey("idx_admin_user_adjustments_legacy_redeem").
			Annotations(entsql.IndexWhere("legacy_redeem_code_id IS NOT NULL")),
	}
}
