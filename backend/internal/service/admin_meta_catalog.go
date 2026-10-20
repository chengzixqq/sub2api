package service

import "github.com/Wei-Shaw/sub2api/internal/domain"

// AdminMetaCatalog is the runtime metadata of GET /api/v1/admin/meta/catalog:
// the platform catalog plus the enumerations clients would otherwise hardcode.
// Every list is built from compile-time constants; nothing is read from the
// database. PlatformAuth is trimmed to what the caller may create.
type AdminMetaCatalog struct {
	PlatformCatalog PlatformCatalog `json:"platform_catalog"`
	// AccountTypes lists every account type accepted by account creation and
	// filtering (CreateAccountRequest.Type).
	AccountTypes []string `json:"account_types"`
	// PlatformAuth maps each concrete platform the caller may create accounts
	// on to the account types its creation form offers. It is a form hint and
	// does not take part in CreateAccount validation.
	PlatformAuth map[string][]string `json:"platform_auth"`
	// AccountStatusFilters lists the status values GET /admin/accounts accepts.
	AccountStatusFilters []string `json:"account_status_filters"`
	// RequestTypes lists the usage request_type filter values.
	RequestTypes []string `json:"request_types"`
	// ErrorPhases lists the standardized ops error phases (phase filter).
	ErrorPhases []string `json:"error_phases"`
	UserRoles   []string `json:"user_roles"`
	// APIProtocols lists the api_protocol values of multi-protocol accounts.
	APIProtocols []string `json:"api_protocols"`
}

var adminMetaAccountTypes = []string{
	AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey,
	AccountTypeUpstream, AccountTypeBedrock, AccountTypeServiceAccount,
}

// adminMetaPlatformAuth mirrors the account type choices of the Web
// CreateAccountModal. Platforms not listed only offer API keys, so a newly
// registered API key provider shows up without a table change. Antigravity's
// "upstream" option is created as an apikey account.
var adminMetaPlatformAuth = map[string][]string{
	PlatformAnthropic:   {AccountTypeOAuth, AccountTypeSetupToken, AccountTypeAPIKey, AccountTypeBedrock, AccountTypeServiceAccount},
	PlatformOpenAI:      {AccountTypeOAuth, AccountTypeAPIKey},
	PlatformGemini:      {AccountTypeOAuth, AccountTypeAPIKey, AccountTypeServiceAccount},
	PlatformAntigravity: {AccountTypeOAuth, AccountTypeAPIKey},
	PlatformGrok:        {AccountTypeOAuth, AccountTypeAPIKey},
}

var adminMetaDefaultPlatformAuth = []string{AccountTypeAPIKey}

// adminMetaAccountStatusFilters follows accountStatusPredicate: the status
// column values, the legacy derived values and the C14 derived values.
var adminMetaAccountStatusFilters = []string{
	StatusActive, AccountListStatusInactive, StatusError,
	AccountListStatusRateLimited, AccountListStatusTempUnschedulable, AccountListStatusUnschedulable,
	AccountListStatusOverloaded, AccountListStatusNeedsAttention, AccountListStatusSchedulableNow,
}

var adminMetaRequestTypes = []RequestType{
	RequestTypeSync, RequestTypeStream, RequestTypeWSV2, RequestTypeCyberBlocked, RequestTypeLive,
}

// adminMetaErrorPhases are the phases classifyOpsErrorLog assigns, in the
// order of the Web error details filter.
var adminMetaErrorPhases = []string{"request", "auth", "account_auth", "routing", "upstream", "network", "internal"}

var adminMetaUserRoles = []string{RoleAdmin, RoleUser, RoleVendor}

var adminMetaAPIProtocols = []string{APIProtocolAdaptive, APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses}

// BuildAdminMetaCatalog returns fresh copies, so callers may modify the
// result. Its JSON is byte-stable for a given scope (the ETag relies on it).
func BuildAdminMetaCatalog(scope Scope) AdminMetaCatalog {
	requestTypes := make([]string, 0, len(adminMetaRequestTypes))
	for _, requestType := range adminMetaRequestTypes {
		requestTypes = append(requestTypes, requestType.String())
	}
	return AdminMetaCatalog{
		PlatformCatalog:      BuildPlatformCatalog(),
		AccountTypes:         append([]string(nil), adminMetaAccountTypes...),
		PlatformAuth:         adminMetaPlatformAuthFor(scope),
		AccountStatusFilters: append([]string(nil), adminMetaAccountStatusFilters...),
		RequestTypes:         requestTypes,
		ErrorPhases:          append([]string(nil), adminMetaErrorPhases...),
		UserRoles:            append([]string(nil), adminMetaUserRoles...),
		APIProtocols:         append([]string(nil), adminMetaAPIProtocols...),
	}
}

// adminMetaPlatformAuthFor keeps the platforms scope may create accounts on:
// all for the station owner, all but those vendors may not create for a
// vendor with account management, none otherwise (including a missing scope).
func adminMetaPlatformAuthFor(scope Scope) map[string][]string {
	out := map[string][]string{}
	if scope.RequireAccountManage() != nil {
		return out
	}
	for _, platform := range domain.ConcretePlatformIDs() {
		if !accountCreatePlatformAllowed(scope, platform) {
			continue
		}
		types, ok := adminMetaPlatformAuth[platform]
		if !ok {
			types = adminMetaDefaultPlatformAuth
		}
		out[platform] = append([]string(nil), types...)
	}
	return out
}
