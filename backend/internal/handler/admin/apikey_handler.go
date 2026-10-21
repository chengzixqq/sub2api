package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// adminAPIKeyCreator generates a key for a user on the admin's behalf (implemented by *service.APIKeyService).
type adminAPIKeyCreator interface {
	AdminCreate(ctx context.Context, userID int64, req service.CreateAPIKeyRequest) (*service.APIKey, error)
}

// AdminAPIKeyHandler handles admin API key management
type AdminAPIKeyHandler struct {
	adminService       service.AdminService
	creator            adminAPIKeyCreator
	concurrencyService *service.ConcurrencyService // GET :id 回填 current_concurrency
}

// NewAdminAPIKeyHandler creates a new admin API key handler
func NewAdminAPIKeyHandler(adminService service.AdminService, apiKeyService *service.APIKeyService) *AdminAPIKeyHandler {
	h := &AdminAPIKeyHandler{adminService: adminService}
	if apiKeyService != nil {
		h.creator = apiKeyService
	}
	return h
}

// ProvideAdminAPIKeyHandler is the wire provider: NewAdminAPIKeyHandler plus the
// concurrency service GET /admin/api-keys/:id reads current_concurrency from.
func ProvideAdminAPIKeyHandler(adminService service.AdminService, apiKeyService *service.APIKeyService, concurrencyService *service.ConcurrencyService) *AdminAPIKeyHandler {
	h := NewAdminAPIKeyHandler(adminService, apiKeyService)
	h.concurrencyService = concurrencyService
	return h
}

// AdminCreateAPIKeyRequest is the body of POST /admin/users/:id/api-keys (0 = unlimited; no custom key).
type AdminCreateAPIKeyRequest struct {
	Name          string   `json:"name" binding:"required"`
	GroupID       *int64   `json:"group_id"`
	Quota         float64  `json:"quota"`
	ExpiresInDays *int     `json:"expires_in_days"`
	IPWhitelist   []string `json:"ip_whitelist"`
	IPBlacklist   []string `json:"ip_blacklist"`
	RateLimit5h   float64  `json:"rate_limit_5h"`
	RateLimit1d   float64  `json:"rate_limit_1d"`
	RateLimit7d   float64  `json:"rate_limit_7d"`
}

// CreateForUser creates an API key for a user on the admin's behalf. The key is generated, then bound to group_id
// through the admin group rules (active group, subscription check, exclusive-group grant); if the binding fails the
// new key is deleted again. The full key is returned only here. Idempotency-Key is honoured when sent.
// POST /api/v1/admin/users/:id/api-keys
func (h *AdminAPIKeyHandler) CreateForUser(c *gin.Context) {
	if h.creator == nil {
		response.Error(c, http.StatusServiceUnavailable, "API key service not available")
		return
	}
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	var req AdminCreateAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.GroupID != nil && *req.GroupID < 0 {
		response.BadRequest(c, "group_id must be non-negative")
		return
	}

	payload := struct {
		UserID int64                    `json:"user_id"`
		Body   AdminCreateAPIKeyRequest `json:"body"`
	}{userID, req}
	executeAdminOptionalIdempotentJSON(c, "admin.api_keys.create", payload, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		created, err := h.creator.AdminCreate(ctx, userID, service.CreateAPIKeyRequest{
			Name: req.Name, Quota: req.Quota, ExpiresInDays: req.ExpiresInDays, IPWhitelist: req.IPWhitelist, IPBlacklist: req.IPBlacklist,
			RateLimit5h: req.RateLimit5h, RateLimit1d: req.RateLimit1d, RateLimit7d: req.RateLimit7d,
		})
		if err != nil {
			return nil, err
		}
		result := &service.AdminUpdateAPIKeyGroupIDResult{APIKey: created}
		if req.GroupID != nil && *req.GroupID > 0 {
			bound, err := h.adminService.AdminUpdateAPIKeyGroupID(ctx, created.ID, req.GroupID)
			if err != nil {
				// Do not leave behind a key the admin did not ask for.
				_ = h.adminService.AdminDeleteAPIKey(ctx, created.ID)
				return nil, err
			}
			result = bound
			if result.APIKey != nil {
				result.APIKey.Key = created.Key
			}
		}
		return adminAPIKeyResponse(result, nil), nil
	})
}

type adminAPIKeyResult struct {
	APIKey                 *dto.APIKey `json:"api_key"`
	AutoGrantedGroupAccess bool        `json:"auto_granted_group_access"`
	GrantedGroupID         *int64      `json:"granted_group_id,omitempty"`
	GrantedGroupName       string      `json:"granted_group_name,omitempty"`
	RevokedGroupAccess     bool        `json:"revoked_group_access,omitempty"`
	RevokedGroupID         *int64      `json:"revoked_group_id,omitempty"`
	// Previous holds the old values of the fields an update changed, for the client's undo.
	Previous map[string]any `json:"previous,omitempty"`
}

func adminAPIKeyResponse(result *service.AdminUpdateAPIKeyGroupIDResult, previous map[string]any) adminAPIKeyResult {
	return adminAPIKeyResult{
		APIKey:                 dto.APIKeyFromService(result.APIKey),
		AutoGrantedGroupAccess: result.AutoGrantedGroupAccess,
		GrantedGroupID:         result.GrantedGroupID,
		GrantedGroupName:       result.GrantedGroupName,
		RevokedGroupAccess:     result.RevokedGroupAccess,
		RevokedGroupID:         result.RevokedGroupID,
		Previous:               previous,
	}
}

// AdminUpdateAPIKeyGroupRequest represents the request to update an API key. Every field is optional (null = unchanged);
// the Web sends only group_id, which keeps its original behaviour and response.
type AdminUpdateAPIKeyGroupRequest struct {
	GroupID             *int64 `json:"group_id"`               // nil=不修改, 0=解绑, >0=绑定到目标分组
	ResetRateLimitUsage *bool  `json:"reset_rate_limit_usage"` // true=重置 5h/1d/7d 限速用量
	// RevokeGroupAccessID withdraws this exclusive group from the user in the same
	// transaction (undo of an auto-grant); 409 GROUP_STILL_IN_USE if another key uses it.
	RevokeGroupAccessID *int64 `json:"revoke_group_access_id"`

	Name        *string   `json:"name"`
	Status      *string   `json:"status"` // active | inactive; the legacy disabled is stored as inactive
	Quota       *float64  `json:"quota"`  // USD, 0 = unlimited
	ResetQuota  *bool     `json:"reset_quota"`
	ExpiresAt   *string   `json:"expires_at"` // RFC3339; "" clears the expiry
	IPWhitelist *[]string `json:"ip_whitelist"`
	IPBlacklist *[]string `json:"ip_blacklist"`
	RateLimit5h *float64  `json:"rate_limit_5h"`
	RateLimit1d *float64  `json:"rate_limit_1d"`
	RateLimit7d *float64  `json:"rate_limit_7d"`
}

// fieldsInput converts the non-group fields; ok is false when none were sent.
func (r AdminUpdateAPIKeyGroupRequest) fieldsInput() (in service.AdminUpdateAPIKeyInput, ok bool, err error) {
	in = service.AdminUpdateAPIKeyInput{
		Name: r.Name, Status: r.Status, Quota: r.Quota, IPWhitelist: r.IPWhitelist, IPBlacklist: r.IPBlacklist,
		RateLimit5h: r.RateLimit5h, RateLimit1d: r.RateLimit1d, RateLimit7d: r.RateLimit7d,
		ResetQuota: r.ResetQuota != nil && *r.ResetQuota,
	}
	if r.ExpiresAt != nil {
		raw := strings.TrimSpace(*r.ExpiresAt)
		if raw == "" {
			in.ClearExpiration = true
		} else {
			t, parseErr := time.Parse(time.RFC3339, raw)
			if parseErr != nil {
				return in, false, parseErr
			}
			utc := t.UTC()
			in.ExpiresAt = &utc
		}
	}
	ok = in.Name != nil || in.Status != nil || in.Quota != nil || in.ResetQuota || in.ExpiresAt != nil || in.ClearExpiration ||
		in.IPWhitelist != nil || in.IPBlacklist != nil || in.RateLimit5h != nil || in.RateLimit1d != nil || in.RateLimit7d != nil
	return in, ok, nil
}

// Get reads one end-user API key (to refresh a key card after an action).
// GET /api/v1/admin/api-keys/:id
func (h *AdminAPIKeyHandler) Get(c *gin.Context) {
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid API key ID")
		return
	}
	masked, ok := parseAPIKeyMaskedView(c)
	if !ok {
		return
	}
	key, err := h.adminService.AdminGetAPIKey(c.Request.Context(), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := dto.APIKeyFromService(key)
	if out != nil {
		out.CurrentConcurrency = apiKeyConcurrencyCounts(c.Request.Context(), h.concurrencyService, []service.APIKey{*key})[key.ID]
	}
	if masked {
		out.HideFullKey()
	}
	response.Success(c, out)
}

// Reveal returns one key's plaintext for an admin who passed step-up. It is a
// POST so the audit log records it, and the response is never cached. Deleted
// (tombstoned) keys have no plaintext left and answer 404.
// POST /api/v1/admin/api-keys/:id/reveal
func (h *AdminAPIKeyHandler) Reveal(c *gin.Context) {
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid API key ID")
		return
	}
	key, err := h.adminService.AdminGetAPIKey(c.Request.Context(), keyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if key == nil || dto.MaskAPIKey(key.Key) == "" {
		response.ErrorFrom(c, service.ErrAPIKeyNotFound)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Success(c, gin.H{"id": key.ID, "key": key.Key})
}

// Delete deletes an end user's API key on the admin's behalf. Deleting an already deleted key also succeeds.
// DELETE /api/v1/admin/api-keys/:id
func (h *AdminAPIKeyHandler) Delete(c *gin.Context) {
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || keyID <= 0 {
		response.BadRequest(c, "Invalid API key ID")
		return
	}
	if err := h.adminService.AdminDeleteAPIKey(c.Request.Context(), keyID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"id": keyID, "deleted": true})
}

// UpdateGroup handles updating an API key's admin-managed fields.
// PUT /api/v1/admin/api-keys/:id
func (h *AdminAPIKeyHandler) UpdateGroup(c *gin.Context) {
	keyID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid API key ID")
		return
	}

	masked, ok := parseAPIKeyMaskedView(c)
	if !ok {
		return
	}
	var req AdminUpdateAPIKeyGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	fields, hasFields, err := req.fieldsInput()
	if err != nil {
		response.BadRequest(c, "Invalid expires_at, use RFC3339 or an empty string to clear")
		return
	}
	var previous map[string]any
	if hasFields {
		updated, err := h.adminService.AdminUpdateAPIKeyFields(c.Request.Context(), keyID, fields)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		previous = updated.Previous
	}

	var resetKey *service.APIKey
	if req.ResetRateLimitUsage != nil && *req.ResetRateLimitUsage {
		resetKey, err = h.adminService.AdminResetAPIKeyRateLimitUsage(c.Request.Context(), keyID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
	}

	if req.RevokeGroupAccessID != nil && *req.RevokeGroupAccessID <= 0 {
		response.BadRequest(c, "revoke_group_access_id must be positive")
		return
	}
	var result *service.AdminUpdateAPIKeyGroupIDResult
	if req.RevokeGroupAccessID != nil {
		opts := service.AdminUpdateAPIKeyGroupOptions{RevokeGroupAccessID: *req.RevokeGroupAccessID}
		result, err = h.adminService.AdminUpdateAPIKeyGroupIDWithOptions(c.Request.Context(), keyID, req.GroupID, opts)
	} else {
		result, err = h.adminService.AdminUpdateAPIKeyGroupID(c.Request.Context(), keyID, req.GroupID)
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if resetKey != nil && req.GroupID == nil {
		result.APIKey = resetKey
	}

	out := adminAPIKeyResponse(result, previous)
	if masked {
		out.APIKey.HideFullKey()
	}
	response.Success(c, out)
}
