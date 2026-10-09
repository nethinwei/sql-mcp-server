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

1. **数据源与导入**：数据源卡片显示已扫描的库数与实体数，连接详情默认折叠，卡片
   可以用键盘选择。点一个数据源列出它的库：逐个扫描，或“扫描未扫描的库”“重新扫描
   过期的库”。扫描在服务端按批次进行（每次两个库），关闭或刷新页面也会继续，回来后
   显示进度；可以停止，也可以只重试失败与未完成的库。超过 7 天的扫描标为“扫描已
   过期”。扫描结果保存在配置库，重启后仍在。下方的表目录在服务端检索所有已扫描的
   库：按库名通配（如 `pay_*`）、表名或说明关键字、状态（未导入 / 工作区未发布 /
   已发布）与“只看字段有差异”筛选，分页显示；外键列分别显示引用了几张表（↗）和
   被几张表引用（↙），点开查看；展开一行查看列、键与索引。勾选跨页保留；整页勾选
   后可“选择全部匹配的 N 张表”，这时保存的是筛选本身，取消勾选的表被排除。底部的
   已选栏显示数量与被当前筛选隐藏的数量，“查看”打开完整清单（分页、搜索、导出 CSV、
   移除）。“导入预览”可以同时授权给角色、把手机号、邮箱、证件号、密钥类列自动脱敏，
   先给出方案预览（见下）再加入工作区；“按规则处理…”把选中的表带到方案页。
   字段有差异的表点 `+新增 −缺失` 同步字段：只被授权字段列表引用的字段会一并移除，
   被行范围、主键或关系引用的字段不能删除，可以直接跳到对应位置修改。
2. **实体**：调整说明、字段别名、脱敏、排除字段、主键、关系与租户策略。说明留空
   时，服务使用数据库中的表注释与列注释（启动或重载时读取），控制台以灰色显示；
   填写后覆盖注释。导入时不会把注释复制进配置，因此之后修改注释仍会生效。改名时
   授权和其他实体的关系会一起更新；删除实体会同时删除指向它的关系。实体按
   `数据源 · 库` 分组显示，名称与表名不同时才在名称后标出表名（`← 表名`）；不同
   数据源或库的同名表各自导入为同名实体，引用写成能唯一确定实体的最短形式（如
   `crm.tenants`），新增实体使已有引用产生歧义时，控制台自动把它们改成限定写法。
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
8. **批量配置**：
   - **按规则配置**：先选任务（导入新表、授权、隐藏或脱敏字段、同步字段），再定
     范围（数据源、库与表名通配、关键字、状态、字段差异，条件同时满足），页面实时
     显示命中数、样例和每个条件单独的命中数；授权可限定字段与行范围，字段规则可
     一键加入常见敏感字段。预览方案列出涉及的库、所依据扫描的时间、合并后的变化
     （可查看每个实体授权或字段的前后对比）、例外和生效方式。例外分三级：阻断（如
     结果校验不通过）必须先排除对应的表或修复；需确认（如没有主键而未授予写动作、
     新表进入已有授权、依据过期扫描删除字段）要勾选“已核对”才能加入；提示只供参考。
     例外可搜索、分页、导出，可逐条或按类排除表，排除可撤销。方案或工作区在预览后
     变化，需要重新计算。方案可以保存为模板：更新已载入的模板或另存为新模板，模板
     有版本记录，期间被他人修改会提示冲突；“重复执行”的模板不会自动修改工作区；
   - **待处理**：字段有差异的表、未导入的表、未扫描的库、从未列出库的数据源，以及
     有新匹配或有未能应用规则的重复执行模板。每项可以“我来处理”、稍后处理（1/7/30
     天后重新打开）或忽略（必须写原因），有处理记录；情况变化后自动重新打开；
   - **Agent 接入**：为每个客户端生成单独的配置 MCP 令牌（命名、可设有效天数，只
     显示一次），查看创建、到期与最近使用时间，单独吊销；复制 Claude Code、Cursor
     等客户端的配置。Agent 可以检索表目录、扫描库、预览方案并保存为草稿（可在已有
     草稿上继续），不能发布；草稿在版本历史中由人审核发布。设计见
     `docs/design/planning.md`。
9. **版本历史**：查看每个版本的作者、说明与差异，发布草稿，或回滚到更早的发布
   内容（回滚生成一个新版本，不改写历史）。

页头的运行状态徽标显示服务实际在用的版本。别人发布新版本后，控制台会立即收到
通知：工作区没有修改时直接切换到新版本，有修改时提示基于新版本重新应用。服务端只
接受基于当前已发布版本的发布，所以不会出现后发布的人整体覆盖前一次发布。重新应用
时，只有一方修改的地方自动合并；双方改了同一处（例如同一字段的脱敏、同一实体的
同一授权）就是冲突，“检查并发布”页会逐项列出双方的值，必须选择保留哪一方，全部
解决后才能保存或发布。

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

## 放在网关或反向代理之后

- **单实例或会话粘滞**：管理员会话在进程内存中，多个副本时必须按 Cookie
  `smcp_admin` 做会话粘滞（或控制台只路由到一个副本），否则请求落到别的副本会被要求
  重新登录。扫描结果、扫描批次、方案模板与配置 MCP 令牌都在配置库里，各副本共享；
  逐个库的单次扫描任务（`startSchemaScan`）仍只在发起它的副本上可查。MCP 端
  （`/mcp`）与配置 MCP（`/admin/mcp`，令牌鉴权）没有会话粘滞的要求。
- **对外地址**：设置 `server.publicURL` 为客户端经网关访问 MCP 端点的完整地址
  （如 `https://gw.example.com/sql-mcp/mcp`）。用户页生成 token 后给出的 Claude
  Code / Cursor / VS Code 接入配置使用它；未设置时退回控制台所在地址加 `/mcp`，
  并提示可能不正确。
- **超时**：`/admin/graphql` 中列库、读表注释在请求内连接数据库，最多等 30 秒；
  网关的上游读超时应不小于 60 秒。扫描整库是后台任务，不受网关超时影响。
- **推送**：`/admin/events` 是长连接（server-sent events），每 25 秒发一次保活。
  网关需关闭该路径的响应缓冲（服务已发送 `X-Accel-Buffering: no`），读超时大于 25
  秒；被切断时控制台退回 10 秒轮询。
- **路径前缀**：网关把服务挂在某个路径下（如 `https://gw.example.com/sql-mcp-server/`）
  时，设置 `server.pathPrefix: /sql-mcp-server`（修改需重启），网关只需剥掉前缀转发：
  控制台页面的 `<base>`、会话 Cookie 的路径与重定向都由服务带上前缀，不需要网关改写
  响应体、Cookie 或 `Location`。此时控制台只能经网关访问。
- **压缩**：服务对 `/admin/graphql` 的 JSON 响应做 gzip（客户端支持时）。页面与脚本
  不压缩，网关可对该路由开启 gzip；仍在用响应改写适配前缀时不要开启（压缩先于改写
  执行，压缩后的内容无法改写），改用 `server.pathPrefix`。
- **请求体**：控制台只提交改动过的配置节；网关仍提示请求体写入临时文件时，可调大
  `client_body_buffer_size`（如 1m），这只是性能提示，不影响请求。
- **出错时**：控制台把网关错误页显示为“网关返回 HTTP 状态码”或“没有连上服务”。
  持续出现时先查服务日志：后台任务（扫描、权限探测、对账）中的 panic 会记录为
  `... panicked` 并带堆栈，任务失败但进程不会退出。

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
