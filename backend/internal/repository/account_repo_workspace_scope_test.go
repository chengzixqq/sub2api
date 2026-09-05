package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

// newWorkspaceScopeAccountRepo 建内存库并预置两个工作区各一个账号。
func newWorkspaceScopeAccountRepo(t *testing.T) (*accountRepository, int64, int64) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	mk := func(name string, workspaceID int64) int64 {
		t.Helper()
		a, err := client.Account.Create().
			SetName(name).
			SetPlatform(service.PlatformOpenAI).
			SetType(service.AccountTypeAPIKey).
			SetCredentials(map[string]any{"api_key": "sk-" + name}).
			SetWorkspaceID(workspaceID).
			Save(context.Background())
		require.NoError(t, err)
		return a.ID
	}

	repo := &accountRepository{client: client, sql: db}
	return repo, mk("in-scope", 1), mk("other-scope", 2)
}

func workspaceScopedCtx(workspaceID int64, unrestricted bool) context.Context {
	return service.WithScope(context.Background(), service.Scope{
		Unrestricted: unrestricted,
		WorkspaceID:  workspaceID,
	})
}

func listScopedAccounts(t *testing.T, repo *accountRepository, ctx context.Context) ([]service.Account, *pagination.PaginationResult) {
	t.Helper()
	accounts, page, err := repo.ListWithFilters(
		ctx, pagination.PaginationParams{Page: 1, PageSize: 50}, "", "", "", "", 0, "",
	)
	require.NoError(t, err)
	return accounts, page
}

func TestGetByIDScopedRejectsCrossWorkspaceAccess(t *testing.T) {
	repo, mine, other := newWorkspaceScopeAccountRepo(t)
	ctx := workspaceScopedCtx(1, false)

	got, err := repo.GetByIDScoped(ctx, mine)
	require.NoError(t, err)
	require.Equal(t, mine, got.ID)

	// 越权读取必须表现为「不存在」，避免暴露其他工作区的资源是否存在。
	_, err = repo.GetByIDScoped(ctx, other)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
}

func TestGetByIDScopedUnrestrictedSeesAllWorkspaces(t *testing.T) {
	repo, mine, other := newWorkspaceScopeAccountRepo(t)
	ctx := workspaceScopedCtx(0, true)

	for _, id := range []int64{mine, other} {
		got, err := repo.GetByIDScoped(ctx, id)
		require.NoError(t, err)
		require.Equal(t, id, got.ID)
	}
}

func TestGetByIDScopedDeniesWhenScopeMissing(t *testing.T) {
	repo, mine, _ := newWorkspaceScopeAccountRepo(t)

	// 无作用域的上下文不得回退成「全量可见」。
	_, err := repo.GetByIDScoped(context.Background(), mine)
	require.Error(t, err)
}

func TestGetByIDIgnoresScopeForGatewayPath(t *testing.T) {
	repo, mine, other := newWorkspaceScopeAccountRepo(t)

	// 网关调度与后台任务不带作用域，必须仍能取到任意账号。
	for _, id := range []int64{mine, other} {
		got, err := repo.GetByID(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, id, got.ID)
	}
}

func TestListWithFiltersScopesToWorkspace(t *testing.T) {
	repo, mine, _ := newWorkspaceScopeAccountRepo(t)

	accounts, page := listScopedAccounts(t, repo, workspaceScopedCtx(1, false))
	require.Len(t, accounts, 1)
	require.Equal(t, mine, accounts[0].ID)
	// 总数必须与过滤后的结果一致，否则分页会出现「本页 1 条、总数 2 条」的错乱。
	require.Equal(t, int64(1), page.Total)

	allAccounts, allPage := listScopedAccounts(t, repo, workspaceScopedCtx(0, true))
	require.Len(t, allAccounts, 2)
	require.Equal(t, int64(2), allPage.Total)
}
