package service

import (
	"context"
	"net/http"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AdminService interface defines admin management operations
type AdminService interface {
	// User management
	ListUsers(ctx context.Context, page, pageSize int, filters UserListFilters, sortBy, sortOrder string) ([]User, int64, error)
	GetUser(ctx context.Context, id int64) (*User, error)
	GetUserIncludeDeleted(ctx context.Context, id int64) (*User, error)
	CreateUser(ctx context.Context, input *CreateUserInput) (*User, error)
	UpdateUser(ctx context.Context, id int64, input *UpdateUserInput) (*User, error)
	DeleteUser(ctx context.Context, id int64) error
	UpdateUserBalance(ctx context.Context, userID int64, balance string, operation string, notes string) (*User, error)
	BatchUpdateConcurrency(ctx context.Context, userIDs []int64, value int, mode string) (int, error)
	BatchUpdateLimits(ctx context.Context, userIDs []int64, concurrency, rpmLimit *int) (int, error)
	GetUserAPIKeys(ctx context.Context, userID int64, page, pageSize int, sortBy, sortOrder string) ([]APIKey, int64, error)
	GetUserUsageStats(ctx context.Context, userID int64, period string) (any, error)
	GetUserRPMStatus(ctx context.Context, userID int64) (*UserRPMStatus, error)
	// GetUserBalanceHistory returns paginated balance/concurrency change records for a user.
	// codeType is optional - pass empty string to return all types.
	// Also returns totalRecharged (sum of all positive balance top-ups).
	GetUserBalanceHistory(ctx context.Context, userID int64, page, pageSize int, codeType string) ([]RedeemCode, int64, float64, error)
	BindUserAuthIdentity(ctx context.Context, userID int64, input AdminBindAuthIdentityInput) (*AdminBoundAuthIdentity, error)

	// Group management

	// FilterGroupIDsByScope 按调用者作用域裁剪分组 ID 集合。
	//
	// 给那些不经 adminService 取数的汇总端点（用量、容量）用：
	// 它们各自走独立 service，无法在此处收窄，只能拿到结果后按本方法过滤。
	// admin 原样返回，vendor 只留已授权分组。
	FilterGroupIDsByScope(ctx context.Context, ids []int64) ([]int64, error)

	// FilterAccountIDsByScope 按调用者作用域裁剪账号 ID 集合。
	//
	// 给批量统计端点用。必须在取数与拼缓存键之前调用：那些端点的缓存键
	// 只由账号 ID 构成，若先取数再裁剪，A 家写入的结果会被 B 家按同一组
	// ID 命中缓存，收窄形同虚设。
	FilterAccountIDsByScope(ctx context.Context, ids []int64) ([]int64, error)

	ListGroups(ctx context.Context, page, pageSize int, platform, status, search string, isExclusive *bool, sortBy, sortOrder string) ([]Group, int64, error)
	GetAllGroups(ctx context.Context) ([]Group, error)
	GetAllGroupsByPlatform(ctx context.Context, platform string) ([]Group, error)
	// GetAllGroupsIncludingInactive returns all groups regardless of status (active + disabled),
	// ordered by sort_order then id. Used by the API Key group filter dropdown.
	GetAllGroupsIncludingInactive(ctx context.Context) ([]Group, error)
	GetGroup(ctx context.Context, id int64) (*Group, error)
	GetGroupModelsListCandidates(ctx context.Context, id int64, platform string) ([]string, error)
	CreateGroup(ctx context.Context, input *CreateGroupInput) (*Group, error)
	// DuplicateGroup creates an inactive independent copy of a group's configuration
	// and account bindings while preserving each binding's priority.
	DuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error)
	// RecoverDuplicateGroup returns a previously committed copy for an ambiguous retry.
	// It never creates a group.
	RecoverDuplicateGroup(ctx context.Context, id int64, actorScope, operationKey string) (*Group, error)
	UpdateGroup(ctx context.Context, id int64, input *UpdateGroupInput) (*Group, error)
	DeleteGroup(ctx context.Context, id int64) error
	ListCompositeRoutes(ctx context.Context, groupID int64) ([]CompositeModelRoute, error)
	CreateCompositeRoute(ctx context.Context, groupID int64, input CompositeRouteInput) (*CompositeModelRoute, error)
	UpdateCompositeRoute(ctx context.Context, groupID, routeID int64, input CompositeRouteInput) (*CompositeModelRoute, error)
	DeleteCompositeRoute(ctx context.Context, groupID, routeID int64) error
	PreviewCompositeRoute(ctx context.Context, groupID int64, input CompositeRoutePreviewRequest) (*CompositeRouteDecision, error)
	GetGroupAPIKeys(ctx context.Context, groupID int64, page, pageSize int) ([]APIKey, int64, error)
	GetGroupRateMultipliers(ctx context.Context, groupID int64) ([]UserGroupRateEntry, error)
	ClearGroupRateMultipliers(ctx context.Context, groupID int64) error
	BatchSetGroupRateMultipliers(ctx context.Context, groupID int64, entries []GroupRateMultiplierInput) error
	ClearGroupRPMOverrides(ctx context.Context, groupID int64) error
	BatchSetGroupRPMOverrides(ctx context.Context, groupID int64, entries []GroupRPMOverrideInput) error
	UpdateGroupSortOrders(ctx context.Context, updates []GroupSortOrderUpdate) error

	// API Key management (admin)
	AdminUpdateAPIKeyGroupID(ctx context.Context, keyID int64, groupID *int64) (*AdminUpdateAPIKeyGroupIDResult, error)
	AdminResetAPIKeyRateLimitUsage(ctx context.Context, keyID int64) (*APIKey, error)

	// ReplaceUserGroup 替换用户的专属分组：授予新分组权限、迁移 Key、移除旧分组权限
	ReplaceUserGroup(ctx context.Context, userID, oldGroupID, newGroupID int64) (*ReplaceUserGroupResult, error)

	// Account management
	ListAccounts(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode string, sortBy, sortOrder string) ([]Account, int64, error)
	// ListAccountsForSchedulerScoreFilter 返回符合过滤条件的全部账号（不分页），
	// 作为账号列表页计算 OpenAI 调度分数的过滤范围池。
	ListAccountsForSchedulerScoreFilter(ctx context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, error)
	// ListOpenAISchedulableAccountsForSchedulerScore 返回指定分组（nil 为未分组）内
	// 可调度的 OpenAI 账号，用于按组计算调度分数。
	ListOpenAISchedulableAccountsForSchedulerScore(ctx context.Context, groupID *int64) ([]Account, error)
	GetAccount(ctx context.Context, id int64) (*Account, error)
	GetAccountsByIDs(ctx context.Context, ids []int64) ([]*Account, error)
	CreateAccount(ctx context.Context, input *CreateAccountInput) (*Account, error)
	// DuplicateAccount creates an independent account from an existing account's configuration.
	// First-class runtime columns are intentionally reset by the normal account creation path.
	DuplicateAccount(ctx context.Context, id int64, actorScope, operationKey string) (*Account, error)
	// RecoverDuplicateAccount returns a previously committed duplicate for an ambiguous retry.
	// It never creates an account.
	RecoverDuplicateAccount(ctx context.Context, id int64, actorScope, operationKey string) (*Account, error)
	UpdateAccount(ctx context.Context, id int64, input *UpdateAccountInput) (*Account, error)
	// UpdateAccountExtra 仅对 Extra 做 JSONB 增量合并（key 级覆盖），不会影响其它字段或运行态键。
	// 用于刷新流程持久化 account_uuid / org_uuid 等少量键，避免被全量快照覆盖。
	UpdateAccountExtra(ctx context.Context, id int64, updates map[string]any) error
	DeleteAccount(ctx context.Context, id int64) error
	RefreshAccountCredentials(ctx context.Context, id int64) (*Account, error)
	ClearAccountError(ctx context.Context, id int64) (*Account, error)
	SetAccountError(ctx context.Context, id int64, errorMsg string) error
	// EnsureOpenAIPrivacy 检查 OpenAI OAuth 账号 privacy_mode，未设置则尝试关闭训练数据共享并持久化。
	EnsureOpenAIPrivacy(ctx context.Context, account *Account) string
	// EnsureAntigravityPrivacy 检查 Antigravity OAuth 账号 privacy_mode，未设置则调用 setUserSettings 并持久化。
	EnsureAntigravityPrivacy(ctx context.Context, account *Account) string
	// ForceOpenAIPrivacy 强制重新设置 OpenAI OAuth 账号隐私，无论当前状态。
	ForceOpenAIPrivacy(ctx context.Context, account *Account) string
	// ForceAntigravityPrivacy 强制重新设置 Antigravity OAuth 账号隐私，无论当前状态。
	ForceAntigravityPrivacy(ctx context.Context, account *Account) string
	SetAccountSchedulable(ctx context.Context, id int64, schedulable bool) (*Account, error)
	BulkUpdateAccounts(ctx context.Context, input *BulkUpdateAccountsInput) (*BulkUpdateAccountsResult, error)
	CheckMixedChannelRisk(ctx context.Context, currentAccountID int64, currentAccountPlatform string, groupIDs []int64) error
	// RevertAccountProxyFallback 将账号的 proxy_id 切回 proxy_fallback_origin_id，并清空 origin 字段。
	// 若账号不存在返回 ErrAccountNotFound；若账号存在但不在 fallback 状态，返回 ErrAccountNotInFallback。
	RevertAccountProxyFallback(ctx context.Context, id int64) error
	// CreateShadow 为指定 OpenAI OAuth 母账号创建 spark 维度影子账号（一母一影）。
	// 影子账号不持凭据（Credentials 恒为空），透传母账号凭据；继承母账号的 ProxyID。
	CreateShadow(ctx context.Context, parentID int64, opts ShadowOptions) (*Account, error)

	// Proxy management
	ListProxies(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]Proxy, int64, error)
	ListProxiesWithAccountCount(ctx context.Context, page, pageSize int, protocol, status, search string, sortBy, sortOrder string) ([]ProxyWithAccountCount, int64, error)
	GetAllProxies(ctx context.Context) ([]Proxy, error)
	GetAllProxiesWithAccountCount(ctx context.Context) ([]ProxyWithAccountCount, error)
	GetProxy(ctx context.Context, id int64) (*Proxy, error)
	GetProxiesByIDs(ctx context.Context, ids []int64) ([]Proxy, error)
	CreateProxy(ctx context.Context, input *CreateProxyInput) (*Proxy, error)
	UpdateProxy(ctx context.Context, id int64, input *UpdateProxyInput) (*Proxy, error)
	DeleteProxy(ctx context.Context, id int64) error
	BatchDeleteProxies(ctx context.Context, ids []int64) (*ProxyBatchDeleteResult, error)
	GetProxyAccounts(ctx context.Context, proxyID int64) ([]ProxyAccountSummary, error)
	CheckProxyExists(ctx context.Context, host string, port int, username, password string) (bool, error)
	TestProxy(ctx context.Context, id int64) (*ProxyTestResult, error)
	CheckProxyQuality(ctx context.Context, id int64) (*ProxyQualityCheckResult, error)

	// Redeem code management
	ListRedeemCodes(ctx context.Context, page, pageSize int, codeType, status, search string, sortBy, sortOrder string) ([]RedeemCode, int64, error)
	GetRedeemCode(ctx context.Context, id int64) (*RedeemCode, error)
	GenerateRedeemCodes(ctx context.Context, input *GenerateRedeemCodesInput) ([]RedeemCode, error)
	DeleteRedeemCode(ctx context.Context, id int64) error
	BatchDeleteRedeemCodes(ctx context.Context, ids []int64) (int64, error)
	ExpireRedeemCode(ctx context.Context, id int64) (*RedeemCode, error)
	ResetAccountQuota(ctx context.Context, id int64) error
}

// CreateUserInput represents input for creating a new user via admin operations.
type CreateUserInput struct {
	Email                string
	Password             string
	Username             string
	Notes                string
	Role                 string // 空字符串表示使用默认角色(user);合法值 admin/user
	Balance              *float64
	Concurrency          int
	RPMLimit             int
	AllowedGroups        []int64
	RestrictPublicGroups bool
	// ActorAdminID 执行本次操作的管理员ID(来自JWT)，仅用于权限敏感操作的审计日志。
	ActorAdminID int64
}

type UpdateUserInput struct {
	Email         string
	Password      string
	Username      *string
	Notes         *string
	Role          string   // 空字符串表示"未提供"(不修改);合法值 admin/user
	Balance       *float64 // 使用指针区分"未提供"和"设置为0"
	Concurrency   *int     // 使用指针区分"未提供"和"设置为0"
	RPMLimit      *int     // 使用指针区分"未提供"和"设置为0"
	Status        string
	AllowedGroups *[]int64 // 使用指针区分"未提供"和"设置为空数组"
	// RestrictPublicGroups 指针区分"未提供"和"显式开关"。
	RestrictPublicGroups *bool
	// GroupRates 用户专属分组倍率配置
	// map[groupID]*rate，nil 表示删除该分组的专属倍率
	GroupRates map[int64]*float64
	// AdjustmentNotes is attached only when Concurrency changes.
	AdjustmentNotes string
	// ActorAdminID 执行本次操作的管理员ID(来自JWT)，仅用于权限敏感操作的审计日志。
	ActorAdminID int64
}

type AdminBindAuthIdentityInput struct {
	ProviderType    string
	ProviderKey     string
	ProviderSubject string
	Issuer          *string
	Metadata        map[string]any
	Channel         *AdminBindAuthIdentityChannelInput
}

type AdminBindAuthIdentityChannelInput struct {
	Channel        string
	ChannelAppID   string
	ChannelSubject string
	Metadata       map[string]any
}

type AdminBoundAuthIdentity struct {
	UserID          int64                          `json:"user_id"`
	ProviderType    string                         `json:"provider_type"`
	ProviderKey     string                         `json:"provider_key"`
	ProviderSubject string                         `json:"provider_subject"`
	VerifiedAt      *time.Time                     `json:"verified_at,omitempty"`
	Issuer          *string                        `json:"issuer,omitempty"`
	Metadata        map[string]any                 `json:"metadata"`
	CreatedAt       time.Time                      `json:"created_at"`
	UpdatedAt       time.Time                      `json:"updated_at"`
	Channel         *AdminBoundAuthIdentityChannel `json:"channel,omitempty"`
}

type AdminBoundAuthIdentityChannel struct {
	Channel        string         `json:"channel"`
	ChannelAppID   string         `json:"channel_app_id"`
	ChannelSubject string         `json:"channel_subject"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type CreateGroupInput struct {
	Name                      string
	Description               string
	Platform                  string
	RateMultiplier            float64
	IsExclusive               bool
	SubscriptionType          string   // standard/subscription
	DailyLimitUSD             *float64 // 日限额 (USD)
	WeeklyLimitUSD            *float64 // 周限额 (USD)
	MonthlyLimitUSD           *float64 // 月限额 (USD)
	LongContextPricingEnabled bool
	ModelPricing              []ChannelModelPricing
	// 图片生成计费配置（仅 antigravity 平台使用）
	AllowImageGeneration         bool
	AllowBatchImageGeneration    bool
	ImageRateIndependent         bool
	ImageRateMultiplier          *float64
	BatchImageDiscountMultiplier *float64
	BatchImageHoldMultiplier     *float64
	VideoRateIndependent         bool
	VideoRateMultiplier          *float64
	// 高峰时段倍率配置（PeakRateMultiplier 为 nil 时按 1.0 处理）
	PeakRateEnabled    bool
	PeakStart          string
	PeakEnd            string
	PeakRateMultiplier *float64
	ImagePrice1K       *float64
	ImagePrice2K       *float64
	ImagePrice4K       *float64
	VideoPrice480P     *float64
	VideoPrice720P     *float64
	VideoPrice1080P    *float64
	// VideoModelPrices 可选按模型族×分辨率覆盖视频每秒单价。
	VideoModelPrices map[string]map[string]float64
	// Codex alpha/search 网页搜索单次价格（USD/次，仅 openai 平台使用）；nil/负数按默认价 0.01 处理
	WebSearchPricePerCall *float64
	// 搜索工具单价 per 1k
	SearchPricePer1k *float64
	// Grok Voice 显式定价（分组级）
	AudioRealtimePricePerMin     *float64
	AudioTTSPricePerMillionChars *float64
	AudioSTTPricePerHour         *float64
	ClaudeCodeOnly               bool   // 仅允许 Claude Code 客户端
	FallbackGroupID              *int64 // 降级分组 ID
	// 无效请求兜底分组 ID（仅 anthropic 平台使用）
	FallbackGroupIDOnInvalidRequest *int64
	// 模型路由配置（仅 anthropic 平台使用）
	ModelRouting        map[string][]int64
	ModelRoutingEnabled bool // 是否启用模型路由
	MCPXMLInject        *bool
	// 支持的模型系列（仅 antigravity 平台使用）
	SupportedModelScopes []string
	// OpenAI Messages 调度配置（仅 openai 平台使用）
	AllowMessagesDispatch       bool
	AllowLive                   bool
	ForceOpenAIFast             bool
	FreeOpenAIFast              bool
	DefaultMappedModel          string
	RequireOAuthOnly            bool
	RequirePrivacySet           bool
	MessagesDispatchModelConfig OpenAIMessagesDispatchModelConfig
	ModelsListConfig            GroupModelsListConfig
	// RPMLimit 分组 RPM 上限（0 = 不限制）
	RPMLimit int
	// MaxReasoningEffort OpenAI/Codex 请求的推理强度上限，空字符串表示不限制。
	MaxReasoningEffort string
	// MaxReasoningEffortOverLimit 超过上限时的访问控制：downgrade（默认）或 deny。
	MaxReasoningEffortOverLimit string
	// ReasoningEffortMappings OpenAI/Codex 推理强度精确映射。
	ReasoningEffortMappings []ReasoningEffortMapping
	// 分组利润控制（五个 token 平台分组可启用；margin/buffer 为小数，nil 按 0 处理）
	ProfitControlEnabled bool
	ProfitMinMargin      *float64
	ProfitSafetyBuffer   *float64
	// 从指定分组复制账号（创建分组后在同一事务内绑定）
	CopyAccountsFromGroupIDs []int64
}

type UpdateGroupInput struct {
	Name                      string
	Description               *string
	Platform                  string
	RateMultiplier            *float64 // 使用指针以支持设置为0
	IsExclusive               *bool
	Status                    string
	SubscriptionType          string   // standard/subscription
	DailyLimitUSD             *float64 // 日限额 (USD)
	WeeklyLimitUSD            *float64 // 周限额 (USD)
	MonthlyLimitUSD           *float64 // 月限额 (USD)
	LongContextPricingEnabled *bool
	ModelPricing              *[]ChannelModelPricing
	// 图片生成计费配置（仅 antigravity 平台使用）
	AllowImageGeneration         *bool
	AllowBatchImageGeneration    *bool
	ImageRateIndependent         *bool
	ImageRateMultiplier          *float64
	BatchImageDiscountMultiplier *float64
	BatchImageHoldMultiplier     *float64
	VideoRateIndependent         *bool
	VideoRateMultiplier          *float64
	// 高峰时段倍率配置（nil 表示不修改）
	PeakRateEnabled    *bool
	PeakStart          *string
	PeakEnd            *string
	PeakRateMultiplier *float64
	ImagePrice1K       *float64
	ImagePrice2K       *float64
	ImagePrice4K       *float64
	VideoPrice480P     *float64
	VideoPrice720P     *float64
	VideoPrice1080P    *float64
	// VideoModelPrices 可选按模型族×分辨率覆盖；nil 表示不修改，空 map 表示清除。
	VideoModelPrices map[string]map[string]float64
	// Codex alpha/search 网页搜索单次价格（USD/次）；nil 表示不修改，负数表示清除回默认价 0.01
	WebSearchPricePerCall *float64
	// 搜索工具单价；nil 不修改，负数清除
	SearchPricePer1k *float64
	// Grok Voice 显式定价；nil 表示不修改，负数表示清除
	AudioRealtimePricePerMin     *float64
	AudioTTSPricePerMillionChars *float64
	AudioSTTPricePerHour         *float64
	ClaudeCodeOnly               *bool  // 仅允许 Claude Code 客户端
	FallbackGroupID              *int64 // 降级分组 ID
	// 无效请求兜底分组 ID（仅 anthropic 平台使用）
	FallbackGroupIDOnInvalidRequest *int64
	// 模型路由配置（仅 anthropic 平台使用）
	ModelRouting        map[string][]int64
	ModelRoutingEnabled *bool // 是否启用模型路由
	MCPXMLInject        *bool
	// 支持的模型系列（仅 antigravity 平台使用）
	SupportedModelScopes *[]string
	// OpenAI Messages 调度配置（仅 openai 平台使用）
	AllowMessagesDispatch       *bool
	AllowLive                   *bool
	ForceOpenAIFast             *bool
	FreeOpenAIFast              *bool
	DefaultMappedModel          *string
	RequireOAuthOnly            *bool
	RequirePrivacySet           *bool
	MessagesDispatchModelConfig *OpenAIMessagesDispatchModelConfig
	ModelsListConfig            *GroupModelsListConfig
	// RPMLimit 分组 RPM 上限（0 = 不限制），nil 表示未提供不改动。
	RPMLimit *int
	// MaxReasoningEffort 空字符串表示清除上限；nil 表示未提供不改动。
	MaxReasoningEffort *string
	// MaxReasoningEffortOverLimit 空字符串视为 downgrade；nil 表示未提供不改动。
	MaxReasoningEffortOverLimit *string
	// ReasoningEffortMappings nil 表示不修改，空数组表示清空，非空数组表示替换。
	ReasoningEffortMappings *[]ReasoningEffortMapping
	// 分组利润控制（nil 表示不修改；margin/buffer 为小数）
	ProfitControlEnabled *bool
	ProfitMinMargin      *float64
	ProfitSafetyBuffer   *float64
	// 从指定分组复制账号（同步操作：先清空当前分组的账号绑定，再绑定源分组的账号）
	CopyAccountsFromGroupIDs []int64
}

type CreateAccountInput struct {
	Name               string
	Notes              *string
	Platform           string
	Type               string
	Credentials        map[string]any
	Extra              map[string]any
	ProxyID            *int64
	Concurrency        int
	Priority           int
	RateMultiplier     *float64 // 账号计费倍率（>=0，允许 0）
	LoadFactor         *int
	GroupIDs           []int64
	ExpiresAt          *int64
	AutoPauseOnExpired *bool
	ProbeEnabled       *bool
	// SkipDefaultGroupBind prevents auto-binding to platform default group when GroupIDs is empty.
	SkipDefaultGroupBind bool
	// SkipMixedChannelCheck skips the mixed channel risk check when binding groups.
	// This should only be set when the caller has explicitly confirmed the risk.
	SkipMixedChannelCheck bool
}

// ShadowOptions is the input for CreateShadow.
// The shadow holds no credentials — the scheduler transparently delegates to the parent account's tokens.
type ShadowOptions struct {
	Name        string
	Priority    int
	Concurrency int
	GroupIDs    []int64
}

type UpdateAccountInput struct {
	Name                  string
	Notes                 *string
	Type                  string // Account type: oauth, setup-token, apikey
	Credentials           map[string]any
	Extra                 map[string]any
	ProxyID               *int64
	Concurrency           *int     // 使用指针区分"未提供"和"设置为0"
	Priority              *int     // 使用指针区分"未提供"和"设置为0"
	RateMultiplier        *float64 // 账号计费倍率（>=0，允许 0）
	LoadFactor            *int
	Status                string
	GroupIDs              *[]int64
	ExpiresAt             *int64
	AutoPauseOnExpired    *bool
	ProbeEnabled          *bool
	RateSyncEnabled       *bool
	SkipMixedChannelCheck bool // 跳过混合渠道检查（用户已确认风险）
}

// BulkUpdateAccountsInput describes the payload for bulk updating accounts.
type BulkUpdateAccountsInput struct {
	AccountIDs     []int64
	Filters        *BulkUpdateAccountFilters
	Name           string
	ProxyID        *int64
	Concurrency    *int
	Priority       *int
	RateMultiplier *float64 // 账号计费倍率（>=0，允许 0）
	LoadFactor     *int
	Status         string
	Schedulable    *bool
	GroupIDs       *[]int64
	Credentials    map[string]any
	Extra          map[string]any
	ProbeEnabled   *bool
	// SkipMixedChannelCheck skips the mixed channel risk check when binding groups.
	// This should only be set when the caller has explicitly confirmed the risk.
	SkipMixedChannelCheck bool
}

type BulkUpdateAccountFilters struct {
	Platform    string
	Type        string
	Status      string
	Group       string
	Search      string
	PrivacyMode string
}

// BulkUpdateAccountResult captures the result for a single account update.
type BulkUpdateAccountResult struct {
	AccountID int64  `json:"account_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// AdminUpdateAPIKeyGroupIDResult is the result of AdminUpdateAPIKeyGroupID.
type AdminUpdateAPIKeyGroupIDResult struct {
	APIKey                 *APIKey
	AutoGrantedGroupAccess bool   // true if a new exclusive group permission was auto-added
	GrantedGroupID         *int64 // the group ID that was auto-granted
	GrantedGroupName       string // the group name that was auto-granted
}

// ReplaceUserGroupResult 分组替换操作的结果
type ReplaceUserGroupResult struct {
	MigratedKeys int64 // 迁移的 Key 数量
}

// UserRPMStatus describes a user's current per-minute RPM usage.
type UserRPMStatus struct {
	UserRPMUsed  int                  `json:"user_rpm_used"`
	UserRPMLimit int                  `json:"user_rpm_limit"`
	PerGroup     []UserGroupRPMStatus `json:"per_group"`
}

// UserGroupRPMStatus describes current per-minute RPM usage for one user/group pair.
type UserGroupRPMStatus struct {
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
	Used      int    `json:"used"`
	Limit     int    `json:"limit"`
	Source    string `json:"source"` // "group" | "override"
}

// BulkUpdateAccountsResult is the aggregated response for bulk updates.
type BulkUpdateAccountsResult struct {
	Success                   int                       `json:"success"`
	Failed                    int                       `json:"failed"`
	SuccessIDs                []int64                   `json:"success_ids"`
	FailedIDs                 []int64                   `json:"failed_ids"`
	Results                   []BulkUpdateAccountResult `json:"results"`
	LongContextInheritedCount int                       `json:"long_context_inherited_count,omitempty"`
}

type CreateProxyInput struct {
	Name           string
	Protocol       string
	Host           string
	Port           int
	Username       string
	Password       string
	ExpiresAt      *time.Time
	FallbackMode   string
	BackupProxyID  *int64
	ExpiryWarnDays int
}

type UpdateProxyInput struct {
	Name           string
	Protocol       string
	Host           string
	Port           int
	Username       string
	Password       string
	Status         string
	ExpiresAt      *time.Time
	FallbackMode   string
	BackupProxyID  *int64
	ExpiryWarnDays int
}

type GenerateRedeemCodesInput struct {
	Count        int
	Type         string
	Value        float64
	GroupID      *int64 // 订阅类型专用：关联的分组ID
	ValidityDays int    // 订阅类型专用：有效天数
	ExpiresAt    *time.Time
}

type ProxyBatchDeleteResult struct {
	DeletedIDs []int64                   `json:"deleted_ids"`
	Skipped    []ProxyBatchDeleteSkipped `json:"skipped"`
}

type ProxyBatchDeleteSkipped struct {
	ID     int64  `json:"id"`
	Reason string `json:"reason"`
}

// ProxyTestResult represents the result of testing a proxy
type ProxyTestResult struct {
	Success     bool   `json:"success"`
	Message     string `json:"message"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	IPAddress   string `json:"ip_address,omitempty"`
	City        string `json:"city,omitempty"`
	Region      string `json:"region,omitempty"`
	Country     string `json:"country,omitempty"`
	CountryCode string `json:"country_code,omitempty"`
}

type ProxyQualityCheckResult struct {
	ProxyID        int64                   `json:"proxy_id"`
	Score          int                     `json:"score"`
	Grade          string                  `json:"grade"`
	Summary        string                  `json:"summary"`
	ExitIP         string                  `json:"exit_ip,omitempty"`
	Country        string                  `json:"country,omitempty"`
	CountryCode    string                  `json:"country_code,omitempty"`
	BaseLatencyMs  int64                   `json:"base_latency_ms,omitempty"`
	PassedCount    int                     `json:"passed_count"`
	WarnCount      int                     `json:"warn_count"`
	FailedCount    int                     `json:"failed_count"`
	ChallengeCount int                     `json:"challenge_count"`
	CheckedAt      int64                   `json:"checked_at"`
	Items          []ProxyQualityCheckItem `json:"items"`
}

type ProxyQualityCheckItem struct {
	Target     string `json:"target"`
	Status     string `json:"status"` // pass/warn/fail/challenge
	HTTPStatus int    `json:"http_status,omitempty"`
	LatencyMs  int64  `json:"latency_ms,omitempty"`
	Message    string `json:"message,omitempty"`
	CFRay      string `json:"cf_ray,omitempty"`
}

// ProxyExitInfo represents proxy exit information from ip-api.com
type ProxyExitInfo struct {
	IP          string
	City        string
	Region      string
	Country     string
	CountryCode string
}

// ProxyExitInfoProber tests proxy connectivity and retrieves exit information
type ProxyExitInfoProber interface {
	ProbeProxy(ctx context.Context, proxyURL string) (*ProxyExitInfo, int64, error)
}

type groupExistenceBatchReader interface {
	ExistsByIDs(ctx context.Context, ids []int64) (map[int64]bool, error)
}

type proxyQualityTarget struct {
	Target          string
	URL             string
	Method          string
	AllowedStatuses map[int]struct{}
}

var proxyQualityTargets = []proxyQualityTarget{
	{
		Target: "openai",
		URL:    "https://api.openai.com/v1/models",
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized: {},
		},
	},
	{
		Target: "anthropic",
		URL:    "https://api.anthropic.com/v1/messages",
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized:     {},
			http.StatusMethodNotAllowed: {},
			http.StatusNotFound:         {},
			http.StatusBadRequest:       {},
		},
	},
	{
		Target: "gemini",
		URL:    "https://generativelanguage.googleapis.com/$discovery/rest?version=v1beta",
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusOK: {},
		},
	},
	{
		Target: "grok",
		URL:    "https://api.x.ai/v1/models",
		Method: http.MethodGet,
		AllowedStatuses: map[int]struct{}{
			http.StatusUnauthorized: {},
		},
	},
}

const (
	proxyQualityRequestTimeout        = 15 * time.Second
	proxyQualityResponseHeaderTimeout = 10 * time.Second
	proxyQualityMaxBodyBytes          = int64(8 * 1024)
	proxyQualityClientUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

var ErrRPMStatusUnavailable = infraerrors.New(http.StatusNotImplemented, "RPM_STATUS_UNAVAILABLE", "RPM cache not available")

// adminServiceImpl implements AdminService
type adminServiceImpl struct {
	userRepo             UserRepository
	groupRepo            GroupRepository
	groupDuplicateRepo   GroupDuplicateRepository
	accountRepo          AccountRepository
	accountDuplicateRepo AccountDuplicateRepository
	accountBillingRepo   AccountBillingSettingsRepository
	proxyRepo            ProxyRepository
	apiKeyRepo           APIKeyRepository
	redeemCodeRepo       RedeemCodeRepository
	adjustmentRepo       AdminUserAdjustmentRepository
	userGroupRateRepo    UserGroupRateRepository
	userRPMCache         UserRPMCache
	billingCacheService  *BillingCacheService
	proxyProber          ProxyExitInfoProber
	proxyLatencyCache    ProxyLatencyCache
	authCacheInvalidator APIKeyAuthCacheInvalidator
	entClient            *dbent.Client // 用于开启数据库事务
	settingService       *SettingService
	defaultSubAssigner   DefaultSubscriptionAssigner
	userSubRepo          UserSubscriptionRepository
	privacyClientFactory PrivacyClientFactory
	runtimeBlocker       AccountRuntimeBlocker
	affiliateService     adminRechargeAffiliateAccruer
	compositeRouteRepo   CompositeModelRouteRepository
	compositeResolver    *CompositeRouteResolver
	workspaceScopeCache  workspaceAccountScopeInvalidator
	// 分组平台变更后用来失效渠道缓存；可为 nil（缓存会在 TTL 到期后自然重建）
	channelCacheInvalidator ChannelCacheInvalidator
}

// ChannelCacheInvalidator 失效渠道缓存。
// 窄接口，避免 admin 服务依赖整个 ChannelService——与 APIKeyAuthCacheInvalidator 同一思路。
type ChannelCacheInvalidator interface {
	InvalidateCache()
}

type adminRechargeAffiliateAccruer interface {
	AccrueInviteRebate(ctx context.Context, inviteeUserID int64, baseRechargeAmount float64) (float64, error)
}

// invalidateWorkspaceAccountScope 安全地失效账号白名单缓存。
//
// 用 *WorkspaceService 装入接口字段时，即便指针为 nil，接口值本身也非 nil，
// 直接调用会 panic。测试与部分装配路径确实会传 nil，因此统一走这里。
// workspaceID 为 0 表示账号不属于任何工作区，无缓存可失效。
func (s *adminServiceImpl) invalidateWorkspaceAccountScope(workspaceID int64) {
	if workspaceID <= 0 {
		return
	}
	if ws, ok := s.workspaceScopeCache.(*WorkspaceService); ok && ws == nil {
		return
	}
	if s.workspaceScopeCache == nil {
		return
	}
	s.workspaceScopeCache.InvalidateAccountIDs(workspaceID)
}

// workspaceAccountScopeInvalidator 在账号归属发生变化后失效账号白名单缓存。
//
// 窄接口而非直接依赖 *WorkspaceService：这里只需要「失效」这一个动作，
// 与 authCacheInvalidator 同一范式。
//
// 必须在账号新建、删除、改变 workspace_id 后调用。漏调的后果不对称：
// 新建漏调只是新账号用量最长 30s 不可见；账号移出工作区漏调则是原 vendor
// 在缓存过期前仍能查到该账号用量 —— 越权。
//
// ListGrantedGroupIDs 一并挂在这里而非另起字段：装配路径与 nil 判定
// 已由 workspaceScopeCache 统一承担，再加一个同源字段只会多一处漏装风险。
type workspaceAccountScopeInvalidator interface {
	InvalidateAccountIDs(workspaceID int64)
	ListGrantedGroupIDs(ctx context.Context, workspaceID int64) ([]int64, error)
	// IsGroupShared 用于共享分组的计费锁定：一个分组被授权给多家工作区时，
	// 任何一家改倍率都会直接改掉其他家的结算口径，因此计费档自动失效。
	IsGroupShared(ctx context.Context, groupID int64) (bool, error)
}

// grantedGroupIDs 返回当前作用域可见的分组 ID 集合。
//
// 返回 nil map 表示不限制（站长）。供应商即便一个分组都没授权，
// 也返回空 map 而非 nil —— 二者语义相反，混淆会让未授权 vendor 看到全站分组。
//
// 工作区服务缺失时返回错误而非放行：分组读路径同时暴露容量、费率与用量，
// 装配疏漏若静默回落为不受限，等于把全站分组交给所有 vendor。
func (s *adminServiceImpl) grantedGroupIDs(ctx context.Context) (map[int64]struct{}, error) {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return nil, nil
	}
	// 两种 nil 都要挡：接口本身为 nil，以及接口装了一个 nil 的
	// *WorkspaceService（此时 s.workspaceScopeCache != nil，调用会 panic）。
	if s.workspaceScopeCache == nil {
		return nil, domain.ErrWorkspaceScopeViolation
	}
	if ws, ok := s.workspaceScopeCache.(*WorkspaceService); ok && ws == nil {
		return nil, domain.ErrWorkspaceScopeViolation
	}
	ids, err := s.workspaceScopeCache.ListGrantedGroupIDs(ctx, scope.WorkspaceID)
	if err != nil {
		return nil, err
	}
	granted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		granted[id] = struct{}{}
	}
	return granted, nil
}

// filterGroupsByScope 按作用域过滤分组切片，原地复用底层数组。
func (s *adminServiceImpl) filterGroupsByScope(ctx context.Context, groups []Group) ([]Group, error) {
	granted, err := s.grantedGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	if granted == nil {
		return groups, nil
	}
	out := make([]Group, 0, len(groups))
	for _, g := range groups {
		if _, ok := granted[g.ID]; ok {
			out = append(out, g)
		}
	}
	if err := s.markSharedGroupBillingLocks(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *adminServiceImpl) markSharedGroupBillingLocks(ctx context.Context, groups []Group) error {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() || !scope.Perms.GroupBilling || len(groups) == 0 {
		return nil
	}
	if s.workspaceScopeCache == nil {
		return domain.ErrWorkspaceScopeViolation
	}
	if ws, ok := s.workspaceScopeCache.(*WorkspaceService); ok && ws == nil {
		return domain.ErrWorkspaceScopeViolation
	}
	for i := range groups {
		shared, err := s.workspaceScopeCache.IsGroupShared(ctx, groups[i].ID)
		if err != nil {
			return err
		}
		groups[i].BillingLocked = shared
	}
	return nil
}

// requireGroupAccess 校验单个分组是否在作用域内。
//
// 越权返回 ErrWorkspaceScopeViolation（对外 404），与账号侧一致：
// 若回 403，vendor 可借错误码差异探测出其他分组的存在性。
func (s *adminServiceImpl) requireGroupAccess(ctx context.Context, groupID int64) error {
	granted, err := s.grantedGroupIDs(ctx)
	if err != nil {
		return err
	}
	if granted == nil {
		return nil
	}
	if _, ok := granted[groupID]; !ok {
		return domain.ErrWorkspaceScopeViolation
	}
	return nil
}

// requireAccountAccess 校验单个账号是否属于调用方工作区。
//
// 存在的理由：账号的状态类操作（清错、清限流、切换可调度、临时不可调度、
// 删除）都只收一个裸 account_id，此前各入口自行决定是否 GetByIDScoped，
// 有几处把 scoped 读放在写之后当回显用 —— 写已落库，读不到也回滚不了。
// 统一收口成前置校验，新增此类入口时照抄一行即可。
//
// 越权返回 ErrWorkspaceScopeViolation（对外 404）而非 403：错误码差异
// 本身就是存在性探针，403 会告诉 vendor「这个 ID 有主」。
//
// 与 requireGroupAccess 的 nil 语义一致：admin 不受限直接放行。
func (s *adminServiceImpl) requireAccountAccess(ctx context.Context, accountID int64) error {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return nil
	}
	// GetByIDScoped 自带工作区过滤：不属于本工作区时返回 not found。
	// 刻意不用 GetByID 再比对 WorkspaceID —— 那要求每个调用方都记得比对，
	// 正是本函数要消灭的那类疏漏。
	account, err := s.accountRepo.GetByIDScoped(ctx, accountID)
	if err != nil {
		return domain.ErrWorkspaceScopeViolation
	}
	if account == nil {
		return domain.ErrWorkspaceScopeViolation
	}
	return nil
}

// filterAccountValuesByScope 把值类型账号切片收窄到调用方工作区名下。
//
// 与 filterAccountsByScope（指针版，admin_account.go）同一判定，
// 只是取数路径返回的是 []Account 而非 []*Account。共用 OwnsWorkspaceID，
// 不查白名单缓存 —— 行内的 WorkspaceID 就是权威值，多查一次只会引入偏差。
//
// 用于共享分组：分组授权判定的是「这个分组可见」，判不了「组内哪些账号
// 是自家的」。A、B 两家共用一个分组时，按 groupID 取到的账号是两家的总和，
// 由此聚合出的任何结果（模型候选、能力清单）都会漏出对方的上游覆盖面。
func (s *adminServiceImpl) filterAccountValuesByScope(ctx context.Context, accounts []Account) []Account {
	scope := ScopeFromContextOrDeny(ctx)
	if !scope.IsVendor() {
		return accounts
	}
	out := make([]Account, 0, len(accounts))
	for i := range accounts {
		if scope.OwnsWorkspaceID(accounts[i].WorkspaceID) {
			out = append(out, accounts[i])
		}
	}
	return out
}

// FilterGroupIDsByScope 按作用域裁剪分组 ID 集合。
//
// 供用量/容量这类走独立 service 的汇总端点使用：它们不经本 service 取数，
// 只能在拿到结果后按此方法剔除未授权分组。
func (s *adminServiceImpl) FilterGroupIDsByScope(ctx context.Context, ids []int64) ([]int64, error) {
	granted, err := s.grantedGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	if granted == nil {
		return ids, nil
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := granted[id]; ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// FilterAccountIDsByScope 按作用域裁剪账号 ID 集合。
//
// 与 FilterGroupIDsByScope 同一范式，但服务对象是批量统计端点，
// 且必须在拼缓存键之前调用 —— 那些端点的键只含账号 ID，
// 先取数再裁剪会让 A 家的结果被 B 家按同一组 ID 命中。
//
// admin 原样返回同一切片；vendor 即便一个账号都没有也返回空切片而非 nil，
// 二者语义相反，混淆会把全部请求的 ID 当成已授权。
// 实现委托给 scopedAccountIDs：按传入 ID 回表比对 workspace_id，
// 与批量写端点走同一判定，避免两套收窄逻辑各自漂移。
func (s *adminServiceImpl) FilterAccountIDsByScope(ctx context.Context, ids []int64) ([]int64, error) {
	return s.scopedAccountIDs(ctx, ids)
}

type userGroupRateBatchReader interface {
	GetByUserIDs(ctx context.Context, userIDs []int64) (map[int64]map[int64]float64, error)
}

// NewAdminService creates a new AdminService
func NewAdminService(
	userRepo UserRepository,
	groupRepo AdminGroupRepository,
	accountRepo AdminAccountRepository,
	proxyRepo ProxyRepository,
	apiKeyRepo APIKeyRepository,
	redeemCodeRepo RedeemCodeRepository,
	adjustmentRepo AdminUserAdjustmentRepository,
	userGroupRateRepo UserGroupRateRepository,
	userRPMCache UserRPMCache,
	billingCacheService *BillingCacheService,
	proxyProber ProxyExitInfoProber,
	proxyLatencyCache ProxyLatencyCache,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	entClient *dbent.Client,
	settingService *SettingService,
	defaultSubAssigner DefaultSubscriptionAssigner,
	userSubRepo UserSubscriptionRepository,
	privacyClientFactory PrivacyClientFactory,
	runtimeBlocker AccountRuntimeBlocker,
	affiliateService *AffiliateService,
	compositeRouteRepo CompositeModelRouteRepository,
	compositeResolver *CompositeRouteResolver,
	workspaceScopeCache *WorkspaceService,
	channelCacheInvalidator ChannelCacheInvalidator,
) AdminService {
	return &adminServiceImpl{
		userRepo:                userRepo,
		groupRepo:               groupRepo,
		groupDuplicateRepo:      groupRepo,
		accountRepo:             accountRepo,
		accountDuplicateRepo:    accountRepo,
		accountBillingRepo:      accountRepo,
		proxyRepo:               proxyRepo,
		apiKeyRepo:              apiKeyRepo,
		redeemCodeRepo:          redeemCodeRepo,
		adjustmentRepo:          adjustmentRepo,
		userGroupRateRepo:       userGroupRateRepo,
		userRPMCache:            userRPMCache,
		billingCacheService:     billingCacheService,
		proxyProber:             proxyProber,
		proxyLatencyCache:       proxyLatencyCache,
		authCacheInvalidator:    authCacheInvalidator,
		entClient:               entClient,
		settingService:          settingService,
		defaultSubAssigner:      defaultSubAssigner,
		userSubRepo:             userSubRepo,
		privacyClientFactory:    privacyClientFactory,
		runtimeBlocker:          runtimeBlocker,
		affiliateService:        affiliateService,
		compositeRouteRepo:      compositeRouteRepo,
		compositeResolver:       compositeResolver,
		workspaceScopeCache:     workspaceScopeCache,
		channelCacheInvalidator: channelCacheInvalidator,
	}
}
