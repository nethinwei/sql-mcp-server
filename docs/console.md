# 管理控制台

管理控制台是嵌入二进制的 Web 界面，用来编辑实体、角色、用户与其他设置，
预览权限效果，并发布或回滚配置版本。它建立在[配置存储](operations.md#配置存储)
之上，所有修改都以 revision 形式记录。设计细节见
[管理 API 设计](design/admin-api.md)。

## 启用条件

- 只在 store 模式下可用（`serve --store`，或设置 `SQL_MCP_STORE`）；文件模式
  （`--config`）不挂载 `/admin`。
- transport 必须是 HTTP，并显式加 `--admin`。控制台在 `http://<addr>/admin/`，
  GraphQL 端点在 `/admin/graphql`；`--admin-playground` 另外提供开发用的
  GraphiQL（`/admin/playground`）。
- store 中必须已有一个已发布的 revision（包含数据源），服务才能启动。
- 管理员认证与 MCP 认证互相独立：MCP token 不能访问 `/admin`，管理员会话也不能
  调用 `/mcp`。

## 首次使用

```sh
export SQL_MCP_STORE=sqlite:/var/lib/sql-mcp-server/config.db

sql-mcp-server store init
sql-mcp-server store import --config config.yaml --comment 初始配置
sql-mcp-server store publish 1
sql-mcp-server admin create --username admin      # 从标准输入读密码
sql-mcp-server serve --admin --watch --transport http --addr 127.0.0.1:8080
```

`config.yaml` 至少要声明数据源；导入 store 前，DSN 中的密码必须写成 `${ENV}`
或 `${file:...}` 占位符（store 拒绝明文密码），`server.auth.token` 必须为空。
`--watch` 让服务轮询新的发布并热加载，控制台发布后通常几秒内生效。

打开 `http://127.0.0.1:8080/admin/`，用刚创建的账号登录。

## 工作流程

控制台的修改先进入浏览器本地的**工作区**（保存在当前浏览器，刷新不丢失），页头
显示工作区基于哪个版本，底部的修改栏列出未保存的修改。确认后在“检查并发布”
页一次性校验、查看差异并发布；发布前不会影响运行中的服务。

1. **数据源与导入**：选择数据源，扫描数据库（可选按 schema 过滤），展开表查看
   每一列的类型、非空、主键与注释，勾选后“加入工作区”。导入时可以直接授权给
   角色；不授权的实体对所有调用方不可见（默认零权限）。表的状态标出已配置、
   已在工作区，以及与数据库不一致的字段（`+新增 −缺失`）。
2. **实体**：调整说明、字段别名、脱敏、排除字段、主键、关系与租户策略。说明留空
   时，服务使用数据库中的表注释与列注释（启动或重载时读取），控制台以灰色显示；
   填写后覆盖注释。导入时不会把注释复制进配置，因此之后修改注释仍会生效。改名时
   授权和其他实体的关系会一起更新；删除实体会同时删除指向它的关系。
3. **角色**：在权限矩阵中为每个实体勾选读、聚合、增、改、删、执行，支持按行或
   按列批量操作；可限制可读/可写字段与行过滤条件。右侧实时预览该角色最终能看到
   的字段和行条件。
4. **用户**：每个用户是一个 MCP 调用方，有独立 token、一个或多个角色、可选的
   直授权限与固定 subject 属性。生成 token 时明文只显示一次，配置中只保存
   `tokenHash`；新 token 在发布后生效。
5. **权限模拟**：选择用户或角色、实体、动作和字段，查看授权结果、覆盖的
   grant、最终行条件；被拒绝时给出原因（包括字段范围分散在多个 grant 时需要
   显式选择字段的提示）。
6. **其他设置**：按配置字段参考编辑 `server`、`cost`、`budget`、`audit` 等其余
   段落，输入时即按服务端相同的规则校验，并标出修改后需要重启的字段。数据源与
   连接串只能用 CLI 修改，避免通过控制台写入凭据。
7. **检查并发布**：服务端完整校验工作区并展示与当前发布版本的差异，可逐项撤销
   某个修改。可热加载的修改发布后自动生效；需要重启的修改（如 `server.addr`、
   `tools`、自定义过程工具的参数）会先提示确认，发布后运行中的实例标记为待重启。
   没有 `admin:publish` 权限时只能保存为草稿。
8. **版本历史**：查看每个版本的作者、说明与差异，发布草稿，或回滚到更早的发布
   内容（回滚生成一个新版本，不改写历史）。

页头的运行状态徽标显示服务实际在用的版本。如果别人在你编辑期间发布了新版本，
控制台会把你的工作区重新基于最新版本；与对方修改冲突的项会单独列出，由你决定
保留哪一边。

## 管理员与权限

| 权限 | 允许的操作 |
| --- | --- |
| `admin:read` | 查看配置、版本与差异 |
| `admin:write` | 编辑工作区、保存草稿、导入预览、权限模拟 |
| `admin:publish` | 发布与回滚 |
| `admin:accounts` | 管理管理员账号（含重置他人密码） |
| `admin:*` | 全部权限 |

管理员账号保存在 store 中，修改立即生效且不进入 revision。可以在控制台的
“管理员”页管理，也可以用 CLI：

```sh
sql-mcp-server admin list
sql-mcp-server admin create --username alice --permissions admin:read,admin:write
sql-mcp-server admin passwd --username alice           # 新密码从标准输入读取
sql-mcp-server admin set --username alice --disable
sql-mcp-server admin set --username alice --permissions admin:*
```

- 任何登录者都可以改自己的密码；改他人密码需要 `admin:accounts`。改密后该账号
  已有的会话全部失效。
- 系统始终保留至少一个启用且有 `admin:accounts` 的账号，控制台与 CLI 都不能
  禁用或降级最后一个；被锁在外面时用 `admin create` 新建账号，或
  `admin set --enable` 重新启用。
- 会话保存在服务进程内存中，空闲 8 小时或登录满 24 小时后失效，服务重启后需要
  重新登录。同一用户名连续 5 次登录失败锁定 1 分钟。

## 安全建议

- 生产环境通过 HTTPS 访问控制台。服务自己终止 TLS（`server.auth.tls.cert`）时
  会话 Cookie 带 `Secure` 标记；由反向代理终止 TLS 时，Cookie 不带该标记，代理
  应只对外提供 HTTPS。
- 监听非 loopback 地址时，MCP 端必须配置认证（`users`、`server.auth.token` 或
  mTLS 客户端证书），否则服务拒绝启动。
- store 中只保存 token hash 与密码占位符；数据库凭据通过环境变量或 secret 文件
  注入，不经过控制台。

## Docker 部署

[`examples/console`](../examples/console) 用 Docker Compose 启动三种受支持的数据库
（PostgreSQL：电商；MySQL：仓储；OceanBase：记账，各自一套覆盖全部特性的业务，
都经只读与读写两个账号接入）并预置实体、角色与用户，以及开启控制台的服务，配置
存储放在 SQLite 数据卷中。服务起来后在该目录运行 `python3 verify.py` 逐项验证。数据、角色、用户 token 与可体验的场景
见该目录的 [README](../examples/console/README.md)。以下命令都在该目录下执行。

```sh
cd examples/console
docker compose up -d --wait db mysql ob              # 启动示例数据库（OceanBase 首次约需 3–5 分钟）
docker compose run --rm mcp store init               # 初始化配置存储
docker compose run --rm mcp store import --config /config/config.yaml --comment 初始配置
docker compose run --rm mcp store publish 1
docker compose run --rm -T mcp admin create --username admin < password.txt
docker compose up -d --wait                          # 启动服务
```

`password.txt` 是一行至少 12 个字符的密码（也可以去掉 `-T` 与重定向，交互输入）。
完成后：

- 控制台：<http://127.0.0.1:8080/admin/>，用 `admin` 登录；
- MCP 端点：`http://127.0.0.1:8080/mcp`。示例用户及其本地测试 token 见
  [README](../examples/console/README.md)（**只用于本示例**，部署前在控制台“用户”
  页重新生成）；
- 健康检查：`curl http://127.0.0.1:8080/healthz`。

Compose 中各项的作用：

- 镜像默认从仓库根目录构建（`sql-mcp-server:local`）；设置 `SQL_MCP_IMAGE`
  可改用发布的镜像，如 `ghcr.io/nethinwei/sql-mcp-server:<版本>`。
- `SQL_MCP_STORE=sqlite:/var/lib/sql-mcp-server/config.db` 指定配置存储，
  该目录挂载为命名卷 `store`，镜像以非 root 用户（uid 65532）运行并拥有此目录。
- 数据库地址与密码通过环境变量（如 `PG_HOST`、`PG_READER_PASSWORD`、
  `PG_WRITER_PASSWORD`，MySQL 与 OceanBase 同理）传入，`config.yaml` 中的 DSN
  只写占位符。
- 服务监听容器内 `0.0.0.0:8080`，端口只映射到宿主机 `127.0.0.1`。对外开放前，
  在前面放一个终止 TLS 的反向代理。

部署到自己的数据库时，修改 `config.yaml` 的 `databases`、改为自己的环境变量，
删除示例的 `entities`、`roles` 与 `users`（之后在控制台导入表、建立角色和用户），
然后重复上面的初始化步骤。配置存储也可以放在 PostgreSQL
或 MySQL 中，把 `SQL_MCP_STORE` 换成对应的 `<driver>:<dsn>` 即可，此时不需要
数据卷。

不使用 Compose 时，等价的 `docker run` 是：

```sh
docker volume create smcp-store
docker run --rm -v smcp-store:/var/lib/sql-mcp-server -v "$PWD/config.yaml:/config/config.yaml:ro" \
  -e SQL_MCP_STORE=sqlite:/var/lib/sql-mcp-server/config.db -e PG_HOST -e PG_READER_PASSWORD -e PG_WRITER_PASSWORD \
  ghcr.io/nethinwei/sql-mcp-server:<版本> store init
# store import / store publish / admin create 同理
docker run -d --name sql-mcp-server -p 127.0.0.1:8080:8080 \
  -v smcp-store:/var/lib/sql-mcp-server -e SQL_MCP_STORE=sqlite:/var/lib/sql-mcp-server/config.db \
  -e PG_HOST -e PG_READER_PASSWORD -e PG_WRITER_PASSWORD \
  ghcr.io/nethinwei/sql-mcp-server:<版本> serve --admin --watch
```

停止并删除示例环境（包括数据卷）：`docker compose down -v`。
