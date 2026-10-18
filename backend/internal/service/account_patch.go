package service

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const accountConfigVersionTagPrefix = "cv-"

// AccountConfigVersionTag renders config_version as the strong ETag returned by
// GET/PATCH /admin/accounts/:id.
func AccountConfigVersionTag(version int64) string {
	return `"` + accountConfigVersionTagPrefix + strconv.FormatInt(version, 10) + `"`
}

// ParseAccountConfigVersionTag accepts one entity tag in the AccountConfigVersionTag
// form (weak and unquoted forms and a bare number are tolerated).
func ParseAccountConfigVersionTag(raw string) (int64, error) {
	tag := strings.TrimSpace(raw)
	tag = strings.TrimPrefix(tag, "W/")
	tag = strings.TrimSuffix(strings.TrimPrefix(tag, `"`), `"`)
	tag = strings.TrimPrefix(tag, accountConfigVersionTagPrefix)
	version, err := strconv.ParseInt(tag, 10, 64)
	if err != nil || version < 0 {
		return 0, infraerrors.BadRequest("INVALID_IF_MATCH", "If-Match must be the account ETag, e.g. \"cv-12\"")
	}
	return version, nil
}

// ErrAccountVersionConflict reports that the account configuration changed since
// the caller read it.
func ErrAccountVersionConflict(current int64) error {
	return infraerrors.New(http.StatusPreconditionFailed, "ACCOUNT_VERSION_CONFLICT",
		"account was modified by someone else; reload and retry").
		WithMetadata(map[string]string{
			"current_config_version": strconv.FormatInt(current, 10),
			"current_etag":           AccountConfigVersionTag(current),
		})
}

// mergeAccountMapPatch applies a top-level key patch: a nil value deletes the key,
// any other value (including nested objects) replaces it. base is not modified.
func mergeAccountMapPatch(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

func validateAccountCredentialsPatch(patch map[string]any) error {
	for key, value := range patch {
		if strings.TrimSpace(key) == "" {
			return infraerrors.BadRequest("INVALID_PATCH", "patch keys must not be empty")
		}
		if value == nil && IsSensitiveCredentialKey(key) {
			return infraerrors.BadRequest("CREDENTIAL_KEY_NOT_DELETABLE",
				"credential key "+key+" cannot be deleted; replace it with a new value instead")
		}
	}
	return nil
}

func validateAccountExtraPatch(patch map[string]any) error {
	for key := range patch {
		if strings.TrimSpace(key) == "" {
			return infraerrors.BadRequest("INVALID_PATCH", "patch keys must not be empty")
		}
	}
	return nil
}

// ApplyAccountMapDiff replays the edit that turned base into desired on top of
// current: keys the edit changed take their desired value, keys it removed are
// removed, and every other key keeps current's value. This lets a patch commit
// against the locked row without rolling back keys written concurrently (token
// refreshes, model rate limits, usage snapshots).
func ApplyAccountMapDiff(current, base, desired map[string]any) map[string]any {
	out := make(map[string]any, len(current)+len(desired))
	for k, v := range current {
		out[k] = v
	}
	for k, v := range desired {
		if old, ok := base[k]; ok && reflect.DeepEqual(old, v) {
			continue
		}
		out[k] = v
	}
	for k := range base {
		if _, kept := desired[k]; !kept {
			delete(out, k)
		}
	}
	return out
}

func cloneAccountMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// AccountPatchBase is what a PATCH edit was computed from. The repository uses it
// to replay only the edit onto the locked row (see ApplyAccountMapDiff) and to
// enforce the If-Match version under that lock.
type AccountPatchBase struct {
	AccountID             int64
	ExpectedConfigVersion *int64
	Credentials           map[string]any
	Extra                 map[string]any
	// StatusEdited is true when the patch sets status; otherwise the row's
	// current status / error_message are kept.
	StatusEdited bool
}

type accountPatchBaseKey struct{}

func withAccountPatchBase(ctx context.Context, base *AccountPatchBase) context.Context {
	return context.WithValue(ctx, accountPatchBaseKey{}, base)
}

// AccountPatchBaseFromContext returns the patch base registered for accountID, so
// writes to other accounts in the same request (spark shadows) are unaffected.
func AccountPatchBaseFromContext(ctx context.Context, accountID int64) (*AccountPatchBase, bool) {
	base, ok := ctx.Value(accountPatchBaseKey{}).(*AccountPatchBase)
	if !ok || base == nil || base.AccountID != accountID {
		return nil, false
	}
	return base, true
}

// managedAccountExtraKeys are owned by dedicated endpoints and background jobs.
// UpdateAccount treats their presence in a full extra map as an explicit request,
// so a merged patch base must not carry them unless the patch names them.
var managedAccountExtraKeys = []string{
	UpstreamBillingProbeEnabledExtraKey,
	UpstreamBillingRateSyncEnabledExtraKey,
	UpstreamBillingProbeExtraKey,
	OllamaCloudUsageSessionExtraKey,
	OllamaCloudUsageAutoRefreshExtraKey,
	OllamaCloudUsageSnapshotExtraKey,
	OpenCodeGoUsageAutoRefreshExtraKey,
	OpenCodeGoUsageSnapshotExtraKey,
}

func withoutManagedAccountExtra(extra, patch map[string]any) map[string]any {
	out := cloneAccountMap(extra)
	for _, key := range managedAccountExtraKeys {
		if _, named := patch[key]; !named {
			delete(out, key)
		}
	}
	return out
}

// deepCloneAccountMap snapshots a JSON-shaped map so later in-place edits of
// nested values cannot leak into the snapshot.
func deepCloneAccountMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return cloneAccountMap(in)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloneAccountMap(in)
	}
	return out
}
