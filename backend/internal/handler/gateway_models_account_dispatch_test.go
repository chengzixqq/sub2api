package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayModelsDispatchFailureRepo struct {
	*gatewayModelsAccountRepoStub
	policy       *service.AccountDispatchPolicy
	policyErr    error
	accountsErr  error
	extraErr     error
	policyReads  int
	failOnReread bool
}

func (r *gatewayModelsDispatchFailureRepo) GetAccountDispatchPolicy(context.Context, int64, int64) (*service.AccountDispatchPolicy, error) {
	r.policyReads++
	if r.failOnReread && r.policyReads > 1 {
		return nil, errors.New("unexpected second authorization read")
	}
	return r.policy, r.policyErr
}

func (r *gatewayModelsDispatchFailureRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]service.Account, error) {
	if r.accountsErr != nil {
		return nil, r.accountsErr
	}
	return r.gatewayModelsAccountRepoStub.ListSchedulableByGroupID(ctx, groupID)
}

func (r *gatewayModelsDispatchFailureRepo) GetByIDs(context.Context, []int64) ([]*service.Account, error) {
	return nil, r.extraErr
}

func (r *gatewayModelsDispatchFailureRepo) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]service.Account, error) {
	accounts, err := r.ListSchedulableByGroupID(ctx, groupID)
	if err != nil {
		return nil, err
	}
	var result []service.Account
	for _, account := range accounts {
		for _, platform := range platforms {
			if account.Platform == platform {
				result = append(result, account)
				break
			}
		}
	}
	return result, nil
}

func TestGeminiV1BetaGetModel_AccountDispatchAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"forced", "composite"} {
		for _, scenario := range []string{"shared", "private without grant", "explicit private member", "policy failure", "account failure", "empty replace"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				groupID := int64(20)
				model := "gemini-4-argon-high"
				account := service.Account{ID: 1, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
					Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
					Credentials: map[string]any{"model_mapping": map[string]any{model: model}}}
				repo := &gatewayModelsDispatchFailureRepo{gatewayModelsAccountRepoStub: &gatewayModelsAccountRepoStub{
					byGroup: map[int64][]service.Account{groupID: {account}},
				}, failOnReread: true}
				wantStatus := http.StatusOK
				switch scenario {
				case "private without grant":
					repo.byGroup[groupID][0].DispatchScope = service.DispatchScopeAuthorizedOnly
					wantStatus = http.StatusNotFound
				case "explicit private member":
					repo.byGroup[groupID][0].DispatchScope = service.DispatchScopeAuthorizedOnly
					repo.policy = &service.AccountDispatchPolicy{Mode: service.AccountDispatchModeReplace,
						Accounts: []service.AccountDispatchGrant{{AccountID: account.ID, Origin: service.AccountDispatchOriginMember}}}
				case "policy failure":
					repo.policyErr = errors.New("authorization unavailable")
					wantStatus = http.StatusServiceUnavailable
				case "account failure":
					repo.accountsErr = errors.New("accounts unavailable")
					wantStatus = http.StatusServiceUnavailable
				case "empty replace":
					repo.policy = &service.AccountDispatchPolicy{Mode: service.AccountDispatchModeReplace}
					wantStatus = http.StatusNotFound
				}
				h := &GatewayHandler{geminiCompatService: service.NewGeminiMessagesCompatService(repo, nil, nil, nil, nil, nil, nil, nil, nil)}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodGet, "/v1beta/models/"+model, nil)
				c.Params = gin.Params{{Key: "model", Value: model}}
				group := &service.Group{ID: groupID, Platform: service.PlatformGemini}
				if route == "forced" {
					c.Set(string(middleware2.ContextKeyForcePlatform), service.PlatformAntigravity)
				} else {
					group.Platform = service.PlatformComposite
					c.Request = c.Request.WithContext(service.WithCompositeRouteDecision(c.Request.Context(), service.CompositeRouteDecision{
						Matched: true, TargetPlatform: service.PlatformAntigravity, UpstreamModel: model,
					}))
				}
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{UserID: 100, GroupID: &groupID, Group: group})
				h.GeminiV1BetaGetModel(c)
				require.Equal(t, wantStatus, rec.Code, rec.Body.String())
				require.Equal(t, 1, repo.policyReads)
				if wantStatus == http.StatusOK {
					require.Contains(t, rec.Body.String(), "models/"+model)
				}
			})
		}
	}
}

func TestGatewayModelCatalogs_AccountDispatchFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, platform := range []string{service.PlatformGemini, service.PlatformComposite} {
		for _, endpoint := range []string{"models", "codex"} {
			for _, scenario := range []string{"policy failure", "account failure", "extra failure", "empty replace", "single policy snapshot", "unmapped defaults"} {
				t.Run(platform+"/"+endpoint+"/"+scenario, func(t *testing.T) {
					groupID := int64(20)
					account := service.Account{ID: 1, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
						Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
						Credentials: map[string]any{"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"}}}
					repo := &gatewayModelsDispatchFailureRepo{gatewayModelsAccountRepoStub: &gatewayModelsAccountRepoStub{
						byGroup: map[int64][]service.Account{groupID: {account}},
					}}
					wantStatus := http.StatusOK
					switch scenario {
					case "policy failure":
						repo.policyErr = errors.New("authorization database unavailable")
						wantStatus = http.StatusServiceUnavailable
					case "account failure":
						repo.accountsErr = errors.New("account database unavailable")
						wantStatus = http.StatusServiceUnavailable
					case "extra failure":
						repo.policy = &service.AccountDispatchPolicy{Mode: service.AccountDispatchModeAppend,
							Accounts: []service.AccountDispatchGrant{{AccountID: 2, Origin: service.AccountDispatchOriginExtra}}}
						repo.extraErr = errors.New("authorized accounts unavailable")
						wantStatus = http.StatusServiceUnavailable
					case "empty replace":
						repo.policy = &service.AccountDispatchPolicy{Mode: service.AccountDispatchModeReplace}
					case "single policy snapshot":
						repo.failOnReread = true
					case "unmapped defaults":
						repo.byGroup[groupID][0].Credentials = nil
					}
					h := newGatewayModelsHandlerForTest(repo)
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
					c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{UserID: 100, GroupID: &groupID,
						Group: &service.Group{ID: groupID, Platform: platform}})
					if endpoint == "codex" {
						h.CodexModels(c)
					} else {
						h.Models(c)
					}
					require.Equal(t, wantStatus, rec.Code, rec.Body.String())
					require.Equal(t, 1, repo.policyReads, "all model catalog lookups must use the bound policy")
					if wantStatus == http.StatusOK {
						var body map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
						key := "data"
						if endpoint == "codex" {
							key = "models"
						}
						var models []json.RawMessage
						require.NoError(t, json.Unmarshal(body[key], &models))
						if scenario == "empty replace" {
							require.Empty(t, models, "empty policy must not resurrect defaults or composite routes")
						} else {
							require.NotEmpty(t, models, "ordinary mapped and unmapped accounts retain their catalog")
						}
					}
				})
			}
		}
	}
}
