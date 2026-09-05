# 交给 GPT 的部署指令

把下面整段（分隔线之间）复制给 GPT / Codex CLI，让它在服务器上完成本次上线。

> 本文档覆盖**两轮改动一次性上线**：工作区机制本体 + 随后修掉的站长建号失败缺陷。两轮都尚未部署，按本文档执行一次即可，不需要分两次。

---

你是一名运维工程师，负责把 sub2api 的一次代码更新部署到生产服务器。这次更新新增了「工作区（供应商代运营）」机制，**包含一次数据库结构变更**。请严格按步骤执行，不要跳步，不要自行优化流程。

## 环境事实（已核实，无需你重新判断）

- 部署方式：Docker Compose，compose 文件在服务器的 sub2api 部署目录（含 `docker-compose.yml` 与 `.env`）
- 三个容器：`sub2api`（应用）、`sub2api-postgres`（PostgreSQL 18）、`sub2api-redis`（Redis 8）
- Postgres 与 Redis **不对外暴露端口**，只能通过 `docker compose exec` 访问
- 应用是**单个 Go 二进制**，前端编译产物被 embed 进二进制。因此前端必须先编译，再编译后端 —— 这一点由 Dockerfile 内部保证，你只需构建镜像
- 数据库连接信息来自 `.env` 的 `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB`（默认值均为 `sub2api`）
- **数据库迁移在应用启动时自动执行**，无需手动跑 SQL。迁移记录在 `schema_migrations` 表，按文件名去重，重复启动不会重复执行
- 本次代码**不在 GitHub 上**。仓库的 `origin` 指向上游官方仓库，本次改动只存在于我本机，尚未提交、也未推送。所以 **`git pull` 拿不到本次代码**，必须按第 3 步传输

## 本次改动范围（供你判断风险，不需要你改代码）

- 后端约 165 个文件、前端约 38 个文件
- 一个新迁移文件 `backend/migrations/192_workspace_vendor_delegation.sql`
- 迁移内容：新建 `workspaces` / `workspace_members` / `workspace_group_grants` 三张表；给 `accounts` / `proxies` 加 `workspace_id NOT NULL DEFAULT 1` 并加外键；给 `audit_logs` 加可空 `workspace_id`；预置一行 `id=1` 的「站长直管」工作区
- 存量数据不搬运：所有既有账号与代理由 `DEFAULT 1` 自动归入站长直管

## 五条硬约束

1. **不要执行 `deploy/docker-deploy.sh`。** 它会从 GitHub 下载 compose 文件覆盖现有配置，你的本地配置和 `.env` 会丢失。
2. **不要执行 `docker compose pull`。** compose 里 `image:` 写的是官方镜像 `weishaw/sub2api:latest`，而本次更新是本地代码，必须本地构建。pull 会把你构建的镜像覆盖回官方版本，改动静默消失。
3. **不要在服务器上 `git pull` 后就直接构建。** 本次代码未推送到远端，`git pull` 只会拿到上游旧代码，构建出的镜像不含本次改动，且不含迁移文件 —— 表面构建成功，实际什么都没更新。
4. **动数据库之前必须先备份，并确认备份文件非空。**
5. **迁移失败时不要重试第二次，也不要手动改数据库。** 立刻停止并把完整报错贴出来等我处理。

## 执行步骤

### 第 1 步：定位并记录现状

进入 sub2api 部署目录（含 `docker-compose.yml` 的那个目录）。执行并把输出贴给我：

```
docker compose ps
docker compose exec -T sub2api /app/sub2api --version 2>/dev/null || echo "版本命令不可用，跳过"
```

记下当前正在运行的镜像 ID，回滚时要用：

```
docker inspect --format='{{.Image}}' sub2api
```

### 第 2 步：备份数据库（不可跳过）

```
docker compose exec -T postgres pg_dump -U sub2api -d sub2api > ~/sub2api-backup-$(date +%Y%m%d-%H%M%S).sql
```

如果 `.env` 里的 `POSTGRES_USER` / `POSTGRES_DB` 不是默认的 `sub2api`，改成实际值。

然后**验证备份有效**——只检查文件存在是不够的：

```
ls -lh ~/sub2api-backup-*.sql | tail -1
tail -5 $(ls -t ~/sub2api-backup-*.sql | head -1)
```

备份文件末尾应出现 `PostgreSQL database dump complete`。若文件小于 100KB 或没有这行，说明备份不完整，**停下来告诉我**，不要继续。

### 第 3 步：取到本次代码

本次代码只在我本机，未提交也未推送，**服务器无法自行获取**。等我把代码传到服务器后再继续 —— 传输方式我会另行告知（可能是打包上传，也可能是我先推到私有分支再让你拉取）。

拿到代码后，确认这三件事，缺任何一件都说明拿到的不是本次代码，**停下来告诉我**：

```
# 1. 迁移文件必须存在
ls -l backend/migrations/192_workspace_vendor_delegation.sql

# 2. 工作区中间件必须存在
ls -l backend/internal/server/middleware/vendor_scope.go

# 3. 站长建号缺陷的修复必须在（应输出 DefaultWorkspaceID 相关行）
grep -n "DefaultWorkspaceID" backend/internal/service/admin_account.go | head -3
```

第 3 项是本次两轮改动里的第二轮。若 grep 无输出，说明只拿到了第一轮，**这种情况下站长会无法新建账号**，必须停下来找我。

### 第 4 步：构建镜像（打新 tag，不要覆盖 latest）

用日期做 tag，便于回滚时区分：

```
cd <仓库根目录>
docker build -t sub2api:workspace-$(date +%Y%m%d) \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --build-arg GOSUMDB=sum.golang.google.cn \
  -f Dockerfile .
```

构建耗时约几分钟（要装前端依赖、编译前端、编译 Go）。构建失败就把完整报错贴出来，**不要**尝试改代码绕过。

### 第 5 步：把 compose 指向新镜像

编辑 `docker-compose.yml`，把 `sub2api` 服务的这一行：

```
    image: weishaw/sub2api:latest
```

改成你刚构建的 tag：

```
    image: sub2api:workspace-<你的日期>
```

改完贴出改动前后两行让我确认。

### 第 6 步：启动并观察迁移

```
docker compose up -d sub2api
docker compose logs -f sub2api
```

盯日志，确认三件事：

1. 没有 `checksum mismatch` —— 出现这个说明迁移文件被改过，立刻停下来告诉我
2. 没有 panic、没有反复重启
3. 服务正常监听

注意：迁移成功时**不会**每条打印一行日志，所以「日志里没看到 192」是正常的，不代表没执行。是否真的执行了，在第 7 步查数据库确认。

看到服务起稳后 `Ctrl-C` 退出日志跟随（这不会停容器）。

### 第 7 步：验证

健康检查（端口取 `.env` 里的 `SERVER_PORT`，没设则是 8080）：

```
PORT=$(grep -E '^SERVER_PORT=' .env | cut -d= -f2); curl -fsS "http://localhost:${PORT:-8080}/health" && echo " OK"
```

确认迁移确实执行了：

```
docker compose exec -T postgres psql -U sub2api -d sub2api -c "SELECT filename FROM schema_migrations WHERE filename LIKE '192%';"
```

必须返回 `192_workspace_vendor_delegation.sql`。返回空表示迁移没跑，停下来告诉我。

确认数据库结构已就位（三张新表 + 加列 + 预置工作区）：

```
docker compose exec -T postgres psql -U sub2api -d sub2api -c "\dt workspace*"
docker compose exec -T postgres psql -U sub2api -d sub2api -c "SELECT id, name, status FROM workspaces;"
docker compose exec -T postgres psql -U sub2api -d sub2api -c "SELECT count(*) AS total, count(*) FILTER (WHERE workspace_id = 1) AS in_ws1 FROM accounts;"
```

预期结果：

- `workspaces`、`workspace_members`、`workspace_group_grants` 三张表存在
- `workspaces` 里有一行 `id=1`、名为「站长直管」、status 为 `active`
- `accounts` 的 `total` 与 `in_ws1` 相等（存量账号全部归入站长直管，这是设计如此）

**然后做这一项功能验证，不要跳过**：用浏览器以站长身份登录后台，**新建一个测试账号**（随便填，建完就删）。这一步专门用来验证第二轮的修复：修复前，站长新建账号会因外键约束失败而报错。建成后确认它落在站长直管工作区：

```
docker compose exec -T postgres psql -U sub2api -d sub2api -c "SELECT id, name, workspace_id FROM accounts ORDER BY id DESC LIMIT 3;"
```

新建那行的 `workspace_id` 必须是 `1`。若建号报错，或 `workspace_id` 是 `0`，说明第二轮修复没进镜像，停下来告诉我。

最后确认容器状态：

```
docker compose ps
```

三个容器都应是 `Up`（`sub2api` 带 `healthy`）。再确认账号列表、分组列表能正常加载。

### 第 8 步：报告

告诉我：新镜像 tag、备份文件路径、迁移是否执行、上面各项验证的实际输出（含新建账号那项）、以及任何异常。

## 出问题怎么回滚

**应用起不来 / 功能异常，但迁移已成功执行**：把 compose 的 `image:` 改回原来的值，`docker compose up -d sub2api`。新增的表和列对旧代码无害（旧代码不读它们），数据不用回滚。

**迁移执行失败或数据异常**：先停应用，再恢复备份，然后回滚镜像：

```
docker compose stop sub2api
cat ~/sub2api-backup-<时间戳>.sql | docker compose exec -T postgres psql -U sub2api -d sub2api
# 改回原 image tag 后：
docker compose up -d sub2api
```

恢复备份前先告诉我你要这么做。

## 遇到这些情况立刻停下来问我，不要自己决定

- 备份文件为空、过小，或结尾没有 `dump complete`
- 第 3 步三项检查有任何一项不通过
- 日志出现 `checksum mismatch`
- 迁移报任何错误
- 站长新建账号失败，或新建的账号 `workspace_id` 不是 1
- 需要修改 `.env`、`config.yaml` 或任何迁移 SQL 文件
- `docker build` 失败
- 想执行 `docker compose down -v`（会删数据卷，绝对不要）
