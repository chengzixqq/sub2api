package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// MetaHandler serves runtime metadata that clients would otherwise hardcode.
type MetaHandler struct{}

func NewMetaHandler() *MetaHandler { return &MetaHandler{} }

// Catalog returns the platform catalog and the enumerations used by account,
// log and user forms. It is computed in memory; the ETag lets clients cache it
// and revalidate with If-None-Match. platform_auth depends on the caller's
// scope, so the ETag differs between the station owner and vendor tiers.
// GET /api/v1/admin/meta/catalog
func (h *MetaHandler) Catalog(c *gin.Context) {
	catalog := service.BuildAdminMetaCatalog(service.ScopeFromContextOrDeny(c.Request.Context()))
	writeSuccessWithETag(c, catalog, catalog)
}
