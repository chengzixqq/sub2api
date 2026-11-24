package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayModelsDispatchFailureRepo struct {
	*gatewayModelsAccountRepoStub
	policy        *service.AccountDispatchPolicy
	userPolicies  map[int64]*service.AccountDispatchPolicy
	extraAccounts map[int64]*service.Account
	policyErr     error
	accountsErr   error
	extraErr      error
	policyReads   int
	groupReads    int
	extraReads    int
	failOnReread  bool
}

func (r *gatewayModelsDispatchFailureRepo) GetAccountDispatchPolicy(_ context.Context, userID, _ int64) (*service.AccountDispatchPolicy, error) {
	r.policyReads++
	if r.failOnReread && r.policyReads > 1 {
		return nil, errors.New("unexpected second authorization read")
	}
	if r.userPolicies != nil {
		return r.userPolicies[userID], r.policyErr
	}
	return r.policy, r.policyErr
}

func (r *gatewayModelsDispatchFailureRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]service.Account, error) {
	r.groupReads++
	if r.accountsErr != nil {
		return nil, r.accountsErr
	}
	return r.gatewayModelsAccountRepoStub.ListSchedulableByGroupID(ctx, groupID)
}

func (r *gatewayModelsDispatchFailureRepo) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	r.extraReads++
	var accounts []*service.Account
	for _, id := range ids {
		if account := r.extraAccounts[id]; account != nil {
			accounts = append(accounts, account)
		}
	}
	return accounts, r.extraErr
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
	for _, platform := range []string{service.PlatformGemini, service.PlatformComposite, service.PlatformAnthropic} {
		for _, endpoint := range []string{"models", "codex"} {
			for _, scenario := range []string{"policy failure", "account failure", "extra failure", "empty replace", "single policy snapshot", "unmapped defaults"} {
				t.Run(platform+"/"+endpoint+"/"+scenario, func(t *testing.T) {
					groupID := int64(20)
					accountPlatform, model := service.PlatformGemini, "gemini-2.5-flash"
					if platform == service.PlatformAnthropic {
						accountPlatform, model = platform, "claude-sonnet-5"
					}
					account := service.Account{ID: 1, Platform: accountPlatform, Type: service.AccountTypeAPIKey,
						Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID},
						Credentials: map[string]any{"model_mapping": map[string]any{model: model}}}
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

func anthropicCatalogRequest(h *GatewayHandler, group *service.Group, userID int64) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{UserID: userID, GroupID: &group.ID, Group: group})
	h.Models(c)
	return rec
}

func TestGatewayModels_AnthropicVariantsUseAuthorizedCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: 20, Platform: service.PlatformAnthropic}
	account := func(id, groupID int64, scope string, mapping map[string]any, window int64) service.Account {
		a := service.Account{ID: id, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, GroupIDs: []int64{groupID}, DispatchScope: scope,
			Credentials: map[string]any{"model_mapping": mapping}}
		a.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{
			Models: map[string]service.UpstreamModelMetadata{"target": {ContextWindow: window}}})
		return a
	}
	shared := account(1, group.ID, "", map[string]any{"shared": "target"}, 899_999)
	private := account(2, group.ID, service.DispatchScopeAuthorizedOnly, map[string]any{"private": "target", "shared": "target"}, 900_000)
	extra := account(3, group.ID+1, service.DispatchScopeAuthorizedOnly, map[string]any{"outside": "target"}, 1_000_000)
	unmapped := account(4, group.ID+1, service.DispatchScopeAuthorizedOnly, nil, 0)
	unmapped.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{
		Models: map[string]service.UpstreamModelMetadata{"claude-fable-5-2": {ContextWindow: 900_000}}})
	memberGrant := service.AccountDispatchGrant{AccountID: private.ID, Origin: service.AccountDispatchOriginMember}
	extraGrant := service.AccountDispatchGrant{AccountID: extra.ID, Origin: service.AccountDispatchOriginExtra}
	repo := &gatewayModelsDispatchFailureRepo{
		gatewayModelsAccountRepoStub: &gatewayModelsAccountRepoStub{byGroup: map[int64][]service.Account{
			group.ID: {shared, private},
		}},
		extraAccounts: map[int64]*service.Account{extra.ID: &extra, unmapped.ID: &unmapped},
		userPolicies: map[int64]*service.AccountDispatchPolicy{
			101: {Mode: service.AccountDispatchModeAppend, Accounts: []service.AccountDispatchGrant{memberGrant, extraGrant}},
			102: {Mode: service.AccountDispatchModeReplace, Accounts: []service.AccountDispatchGrant{extraGrant}},
			103: {Mode: service.AccountDispatchModeReplace},
			104: {Mode: service.AccountDispatchModeReplace, Accounts: []service.AccountDispatchGrant{
				{AccountID: extra.ID, Origin: service.AccountDispatchOriginMember}}},
			105: {Mode: service.AccountDispatchModeAppend, Accounts: []service.AccountDispatchGrant{
				{AccountID: unmapped.ID, Origin: service.AccountDispatchOriginExtra}}},
		},
	}
	h := newGatewayModelsHandlerForTest(repo)
	for _, tc := range []struct {
		name       string
		userID     int64
		allowlist  []string
		routing    map[string][]int64
		want       []string
		extraReads int
	}{
		{"append member and outside grant", 101, nil, nil, []string{"outside", "private", "shared", "outside[1m]", "private[1m]", "shared[1m]"}, 1},
		{"another user in the same group", 100, nil, nil, []string{"shared"}, 0},
		{"routing and allowlist order", 101, []string{"shared[1m]", "outside[1m]", "private[1m]", "shared"}, map[string][]int64{"shared": {shared.ID}}, []string{"outside[1m]", "private[1m]", "shared"}, 1},
		{"replace outside only", 102, nil, nil, []string{"outside", "outside[1m]"}, 1},
		{"replace empty", 103, nil, nil, []string{}, 0},
		{"replace empty with default allowlist", 103, []string{"claude-fable-5-2", "claude-fable-5-2[1m]"}, nil, []string{}, 0},
		{"stale member grant outside group", 104, nil, nil, []string{}, 1},
		{"allowlisted default variant with unmapped grant", 105, []string{"claude-fable-5-2[1m]", "shared"}, nil, []string{"claude-fable-5-2[1m]", "shared"}, 1},
		{"same allowlist without unmapped grant", 100, []string{"claude-fable-5-2[1m]", "shared"}, nil, []string{"shared"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group.ModelAllowlist = service.GroupModelAllowlist{Enabled: tc.allowlist != nil, Models: tc.allowlist}
			group.ModelRoutingEnabled, group.ModelRouting = tc.routing != nil, tc.routing
			repo.policyReads, repo.groupReads, repo.extraReads = 0, 0, 0
			rec := anthropicCatalogRequest(h, group, tc.userID)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var got gatewayModelsResponseForTest
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			require.Equal(t, tc.want, modelIDsForTest(got.Data))
			require.Equal(t, 1, repo.policyReads)
			require.Equal(t, 1, repo.groupReads)
			require.Equal(t, tc.extraReads, repo.extraReads)
		})
	}
}

func TestGatewayModels_AnthropicDefaultVariantsKeepDefaultEntries(t *testing.T) {
	group := &service.Group{ID: 20, Platform: service.PlatformAnthropic}
	a := service.Account{ID: 1, Platform: service.PlatformAnthropic, GroupIDs: []int64{group.ID}, Status: service.StatusActive, Schedulable: true}
	a.SetUpstreamModelMetadataSnapshot(service.UpstreamModelMetadataSnapshot{
		Models: map[string]service.UpstreamModelMetadata{"claude-fable-5-2": {ContextWindow: 1_000_000}}})
	repo := &gatewayModelsDispatchFailureRepo{gatewayModelsAccountRepoStub: &gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{group.ID: {a}},
	}}
	rec := anthropicCatalogRequest(newGatewayModelsHandlerForTest(repo), group, 100)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct{ Data []claude.Model }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Data, len(claude.DefaultModels)+1)
	require.Equal(t, claude.DefaultModels, body.Data[:len(claude.DefaultModels)])
	require.Equal(t, "claude-fable-5-2[1m]", body.Data[len(claude.DefaultModels)].ID)
}
