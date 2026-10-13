package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestPaymentProviders_CreateUpdateListNeverReturnSecrets(t *testing.T) {
	db, err := sql.Open("sqlite", "file:admin_payment_dto?mode=memory&cache=shared")
	require.NoError(t, err)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Schema.Create(context.Background()))
	configSvc := service.NewPaymentConfigService(client, nil, nil)
	svc := service.NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, configSvc, nil, nil, nil)
	handler := NewPaymentHandler(svc, configSvc)
	router := gin.New()
	router.POST("/providers", handler.CreateProvider)
	router.PUT("/providers/:id", handler.UpdateProvider)
	router.GET("/providers", handler.ListProviders)
	call := func(method, path, body string, code int) json.RawMessage {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, code, recorder.Code, recorder.Body.String())
		require.NotContains(t, recorder.Body.String(), "private-secret")
		require.NotContains(t, recorder.Body.String(), "public-secret")
		require.NotContains(t, recorder.Body.String(), "md5-secret")
		var response struct {
			Data json.RawMessage `json:"data"`
		}
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		return response.Data
	}
	response := call(http.MethodPost, "/providers", `{"provider_key":"easypay","name":"V2 draft","supported_types":["alipay"],"enabled":false,"config":{"apiVersion":"v2","pid":"merchant","privateKey":"private-secret","publicKey":"public-secret","pkey":"md5-secret"}}`, http.StatusCreated)
	var created, updated service.ProviderInstanceResponse
	var listed []service.ProviderInstanceResponse
	require.NoError(t, json.Unmarshal(response, &created))
	id := created.ID
	require.NoError(t, json.Unmarshal(call(http.MethodPut, "/providers/"+strconv.FormatInt(id, 10), `{"config":{"privateKey":"","publicKey":"","pkey":""}}`, http.StatusOK), &updated))
	require.NoError(t, json.Unmarshal(call(http.MethodGet, "/providers", "", http.StatusOK), &listed))
	require.Len(t, listed, 1)
	for _, dto := range []service.ProviderInstanceResponse{created, updated, listed[0]} {
		config := dto.Config
		require.Equal(t, "v2", config["apiVersion"])
		require.Equal(t, "merchant", config["pid"])
		require.NotContains(t, config, "privateKey")
		require.NotContains(t, config, "publicKey")
		require.NotContains(t, config, "pkey")
		require.Equal(t, []string{"alipay"}, dto.SupportedTypes)
	}
	inst, err := client.PaymentProviderInstance.Get(context.Background(), id)
	require.NoError(t, err)
	require.Contains(t, inst.Config, "private-secret", "blank edits must preserve stored credentials")
}
