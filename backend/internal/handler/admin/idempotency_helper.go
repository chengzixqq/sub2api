package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

type idempotencyStoreUnavailableMode int

const (
	idempotencyStoreUnavailableFailClose idempotencyStoreUnavailableMode = iota
	idempotencyStoreUnavailableFailOpen
)

// executeAdminIdempotent requires an Idempotency-Key once observe_only is off. Use it only where every caller sends a
// key (the Web already does for duplicate account / group / channel monitor); Web writes that send no key use
// executeAdminOptionalIdempotent instead.
func executeAdminIdempotent(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	return executeAdminIdempotentWithTimeout(c, scope, payload, ttl, 0, execute)
}

func executeAdminIdempotentWithTimeout(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	return executeAdminIdempotentWithOptions(c, scope, payload, ttl, executionTimeout, true, false, execute)
}

func executeAdminIdempotentWithOptions(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	requireKey bool,
	reclaimStale bool,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	coordinator := service.DefaultIdempotencyCoordinator()
	if coordinator == nil {
		ctx := c.Request.Context()
		if executionTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), executionTimeout)
			defer cancel()
		}
		data, err := execute(ctx)
		if err != nil {
			return nil, err
		}
		return &service.IdempotencyExecuteResult{Data: data}, nil
	}

	return coordinator.Execute(c.Request.Context(), service.IdempotencyExecuteOptions{
		Scope:                  scope,
		ActorScope:             adminActorScope(c),
		Method:                 c.Request.Method,
		Route:                  c.FullPath(),
		IdempotencyKey:         c.GetHeader("Idempotency-Key"),
		Payload:                payload,
		RequireKey:             requireKey,
		TTL:                    ttl,
		ExecutionTimeout:       executionTimeout,
		ReclaimStaleProcessing: reclaimStale,
	}, execute)
}

// executeAdminOptionalIdempotent is the optional-key variant of executeAdminIdempotent: a request without a key runs
// directly even when keys are enforced, a keyed request replays. It returns the coordinator outcome so the handler can
// post-process it (account creation schedules probes and maps the mixed-channel conflict).
func executeAdminOptionalIdempotent(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	return executeAdminIdempotentWithOptions(c, scope, payload, ttl, 0, false, false, execute)
}

func adminActorScope(c *gin.Context) string {
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	return actorScope
}

// executeAdminIdempotentJSON requires an Idempotency-Key once observe_only is off. Use it only for endpoints whose
// callers all send a key (balance adjustments, subscription bulk actions and the like); writes the Web sends without
// a key use executeAdminOptionalIdempotentJSON.
func executeAdminIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, 0, idempotencyStoreUnavailableFailClose, execute)
}

func executeAdminIdempotentJSONWithTimeout(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, executionTimeout, idempotencyStoreUnavailableFailClose, execute)
}

func idempotencyBusinessResultCommitted(result *service.IdempotencyExecuteResult, err error) bool {
	if result == nil || !isIdempotencyStoreUnavailable(err) {
		return false
	}
	return infraerrors.FromError(err).Metadata["stage"] == "mark_succeeded"
}

// executeAdminOptionalIdempotentJSON preserves legacy callers that do not send
// a key while making retries deterministic for callers that do.
func executeAdminOptionalIdempotentJSON(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminOptionalIdempotentJSONWithOptions(c, scope, payload, ttl, 0, false, execute)
}

// adminMoneyWriteExecutionTimeout bounds money writes (balance adjustments, redeem-code generation, create-and-redeem)
// once accepted: they keep running and store their result even if the client disconnects. Keep it below the reverse
// proxy timeout; a client cut off earlier gets the stored result by retrying with the same Idempotency-Key.
const adminMoneyWriteExecutionTimeout = 30 * time.Second

// executeAdminMoneyWriteJSON runs a money write that requires an Idempotency-Key (when keys are enforced). Execution
// is detached from the client, and a retry may take over a processing record whose executor died — callers must
// dedupe a re-run in the business layer (balance: action_id; create-and-redeem: code).
func executeAdminMoneyWriteJSON(c *gin.Context, scope string, payload any, execute func(context.Context) (any, error)) {
	result, err := executeAdminIdempotentWithOptions(c, scope, payload, service.DefaultWriteIdempotencyTTL(), adminMoneyWriteExecutionTimeout, true, true, execute)
	writeAdminIdempotentResult(c, scope, result, err, idempotencyStoreUnavailableFailClose, execute)
}

// executeAdminOptionalMoneyWriteJSON is the optional-key variant for money writes the Web sends without a key
// (redeem-code generation). It detaches execution but does not reclaim stale records: a re-run would create codes twice.
func executeAdminOptionalMoneyWriteJSON(c *gin.Context, scope string, payload any, execute func(context.Context) (any, error)) {
	executeAdminOptionalIdempotentJSONWithOptions(c, scope, payload, service.DefaultWriteIdempotencyTTL(), adminMoneyWriteExecutionTimeout, false, execute)
}

// executeAdminOptionalBatchMoneyWrite is the optional-key money write for redeem-code generation once the batch id
// dedupes a re-run: execution is detached and a retry may take over a dead execution. It does not write the response,
// so the handler can post-process a replay (reload values the stored response redacted); use writeAdminIdempotentResult.
func executeAdminOptionalBatchMoneyWrite(c *gin.Context, scope string, payload any, execute func(context.Context) (any, error)) (*service.IdempotencyExecuteResult, error) {
	return executeAdminIdempotentWithOptions(c, scope, payload, service.DefaultWriteIdempotencyTTL(), adminMoneyWriteExecutionTimeout, false, true, execute)
}

// Execution caps for optional-key admin writes (users, groups, account edits). They apply only to requests that send
// an Idempotency-Key: such a request keeps running and stores its result even if the client disconnects, so a retry
// with the same key replays it. Keep them below the reverse proxy timeout.
const (
	adminWriteExecutionTimeout        = 30 * time.Second  // user / group writes, single account edit and schedulable toggle
	adminBulkWriteExecutionTimeout    = 60 * time.Second  // bulk account update and single credential refresh (upstream OAuth)
	adminBatchRefreshExecutionTimeout = 120 * time.Second // batch credential refresh (upstream OAuth per account)
)

// keyedExecutionTimeout returns timeout only when the request carries a non-empty Idempotency-Key. Keyless requests
// (the Web) keep running on the request context with no extra deadline, exactly as before.
func keyedExecutionTimeout(c *gin.Context, timeout time.Duration) time.Duration {
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) == "" {
		return 0
	}
	return timeout
}

// executeAdminOptionalWrite runs a write that accepts an optional Idempotency-Key. Without a key it runs directly on
// the request context, unchanged. With a key a retry replays the stored response, and execution is detached from the
// client and bounded by timeout. Stale processing records are not reclaimed: these writes have no business-level
// dedupe, so re-running one could repeat it (or refresh an upstream token twice). It returns the coordinator outcome
// without writing the response, so the handler can map its own errors first; use writeAdminIdempotentResult.
func executeAdminOptionalWrite(
	c *gin.Context,
	scope string,
	payload any,
	timeout time.Duration,
	execute func(context.Context) (any, error),
) (*service.IdempotencyExecuteResult, error) {
	return executeAdminIdempotentWithOptions(c, scope, payload, service.DefaultWriteIdempotencyTTL(), keyedExecutionTimeout(c, timeout), false, false, execute)
}

// executeAdminOptionalWriteJSON is executeAdminOptionalWrite plus the shared response path (fail-close when the
// idempotency store is unavailable for a keyed request).
func executeAdminOptionalWriteJSON(
	c *gin.Context,
	scope string,
	payload any,
	timeout time.Duration,
	execute func(context.Context) (any, error),
) {
	result, err := executeAdminOptionalWrite(c, scope, payload, timeout, execute)
	writeAdminIdempotentResult(c, scope, result, err, idempotencyStoreUnavailableFailClose, execute)
}

func executeAdminOptionalIdempotentJSONWithOptions(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	reclaimStale bool,
	execute func(context.Context) (any, error),
) {
	// Same response path as the required variant; only a missing key is treated differently (runs directly).
	result, err := executeAdminIdempotentWithOptions(c, scope, payload, ttl, executionTimeout, false, reclaimStale, execute)
	writeAdminIdempotentResult(c, scope, result, err, idempotencyStoreUnavailableFailClose, execute)
}

func executeAdminIdempotentJSONFailOpenOnStoreUnavailable(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	execute func(context.Context) (any, error),
) {
	executeAdminIdempotentJSONWithMode(c, scope, payload, ttl, 0, idempotencyStoreUnavailableFailOpen, execute)
}

func executeAdminIdempotentJSONWithMode(
	c *gin.Context,
	scope string,
	payload any,
	ttl time.Duration,
	executionTimeout time.Duration,
	mode idempotencyStoreUnavailableMode,
	execute func(context.Context) (any, error),
) {
	result, err := executeAdminIdempotentWithTimeout(c, scope, payload, ttl, executionTimeout, execute)
	writeAdminIdempotentResult(c, scope, result, err, mode, execute)
}

// writeAdminIdempotentResult turns a coordinator outcome into the HTTP response (replay / degraded headers,
// Retry-After on conflicts, optional fail-open re-execution when the store is unavailable).
func writeAdminIdempotentResult(
	c *gin.Context,
	scope string,
	result *service.IdempotencyExecuteResult,
	err error,
	mode idempotencyStoreUnavailableMode,
	execute func(context.Context) (any, error),
) {
	if idempotencyBusinessResultCommitted(result, err) {
		c.Header("X-Idempotency-Degraded", "result-committed")
		response.Success(c, result.Data)
		return
	}
	if err != nil {
		if isIdempotencyStoreUnavailable(err) {
			strategy := "fail_close"
			if mode == idempotencyStoreUnavailableFailOpen {
				strategy = "fail_open"
			}
			service.RecordIdempotencyStoreUnavailable(c.FullPath(), scope, "handler_"+strategy)
			logger.LegacyPrintf("handler.idempotency", "[Idempotency] store unavailable: method=%s route=%s scope=%s strategy=%s", c.Request.Method, c.FullPath(), scope, strategy)
			if mode == idempotencyStoreUnavailableFailOpen {
				data, fallbackErr := execute(c.Request.Context())
				if fallbackErr != nil {
					response.ErrorFrom(c, fallbackErr)
					return
				}
				c.Header("X-Idempotency-Degraded", "store-unavailable")
				response.Success(c, data)
				return
			}
		}
		if retryAfter := service.RetryAfterSecondsFromError(err); retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		response.ErrorFrom(c, err)
		return
	}
	writeAdminIdempotentSuccessHeaders(c, result)
	response.Success(c, result.Data)
}

// isIdempotencyStoreUnavailable matches the store outage by reason, not by status: a business error that happens to be
// a 503 (for example a disabled feature) must not be counted as an outage or re-executed by the fail-open mode.
func isIdempotencyStoreUnavailable(err error) bool {
	return err != nil && infraerrors.Reason(err) == infraerrors.Reason(service.ErrIdempotencyStoreUnavail)
}

// writeAdminIdempotentSuccessHeaders marks replays and tells the client until when the key replays, after which only
// the ledger (or other durable records) can answer whether the write happened.
func writeAdminIdempotentSuccessHeaders(c *gin.Context, result *service.IdempotencyExecuteResult) {
	if result == nil {
		return
	}
	if result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	if !result.ExpiresAt.IsZero() {
		c.Header("X-Idempotency-Expires-At", result.ExpiresAt.UTC().Format(time.RFC3339))
	}
}
