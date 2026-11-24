# restockd

自托管的 VPS 补货监控 + 自动锁单服务。单个 Go 二进制，内嵌 Vue 3 管理界面，SQLite 存储。

- 多站点监控：商品列表、规格、各周期价格、配置项、库存（数量）
- 关注规则：指定周期 / 配置 / 系统 / 优惠码 / 支付方式 / 价格上限 / 锁单次数
- 自动锁单：有货即用托管账号下单，生成**未支付**账单（不会自动付款），Telegram / 邮件通知付款链接
- 账号托管：粘贴 Cookie（请求头 / Cookie-Editor JSON / cookies.txt），会话滚动刷新自动回写，失效提醒
- 验证码：统一 Solver 链 —— 外部 HTTP 求解服务 → FlareSolverr（Cloudflare）→ 人工收件箱（界面粘贴 token / Cookie）
- 实时：SSE 推送事件、站点状态、订单、待处理验证

## 架构

```
cmd/restockd            入口：配置、首启预置站点、HTTP 服务
internal/domain         统一领域模型（Product / Price / Stock / Watch / Order ...）
internal/provider       Provider 接口与注册表
  ├─ whmcs              通用 WHMCS（Lagom / standard_cart），按商品组监控，购物车流程锁单
  ├─ usvps24            usvps24.com（Laravel + Inertia），公开 JSON 监控，/cart/checkout 锁单
  └─ htmlutil           表单序列化、金额 / 周期解析、验证码探测
internal/fetch          抓取抽象：直连 / FlareSolverr / Hybrid（先直连，遇质询回落），Cookie 解析
internal/solver         验证码 Solver 接口：Chain、External、FlareSolverr、ManualBroker
internal/engine         每站点调度协程、快照 diff、关注匹配、锁单执行、通知、事件总线
internal/notify         Telegram / SMTP，Hub 按事件类型过滤分发
internal/store          SQLite（WAL），JSON 文档存储
internal/api            REST + SSE + 内嵌静态资源（契约见 docs/api.md）
web/                    Vue 3 + naive-ui 前端
```

### 新增一个主机商

1. 新建 `internal/provider/<name>/`，实现 `provider.Provider`：
   - `Meta()`：Kind、能力声明、最短 / 默认轮询间隔、默认抓取方式、配置字段（前端据此自动生成表单）、可能出现的验证码类型
   - `Catalog(ctx, env)`：返回 `domain.Catalog`（商品、价格、库存、配置项）
2. 可选实现 `provider.Orderer`（`Quote` 算价 / 校验优惠码、`PlaceOrder` 锁单，`spec.DryRun` 时停在提交前）
   与 `provider.SessionChecker`（校验 Cookie 是否有效）。
3. 在 `init()` 中 `provider.Register(meta, factory)`，并在 `cmd/restockd/main.go` 里空导入。
4. 所有网络请求经 `env.Client.Do(ctx, env.Session, req)`；遇到验证码构造 `solver.Challenge` 交给 `env.Solver`。
   使用 WHMCS 的商家无需写代码，在「站点」页选 `whmcs` 填商品组即可。

## 本地开发

前端产物通过 `go:embed` 内嵌，首次 `go build` 前需先构建一次前端：

```bash
make web                        # npm ci + 构建 web/dist
make test                       # Go（-race）+ 前端单测
cd web && npm install && npm run dev   # 前端 http://localhost:5173，/api 代理到 8080
RESTOCKD_PASSWORD=dev go run ./cmd/restockd -debug
```

## 部署（systemd）

```bash
make build                      # 生成 bin/restockd-linux-amd64
```

服务器上：

```bash
useradd --system --home /var/lib/restockd --shell /usr/sbin/nologin restockd
install -d -o restockd -g restockd -m 700 /var/lib/restockd
install -D -m 755 restockd-linux-amd64 /opt/restockd/restockd
install -D -m 600 restockd.env.example /etc/restockd/restockd.env   # 修改密码
install -m 644 restockd.service /etc/systemd/system/restockd.service
systemctl daemon-reload && systemctl enable --now restockd
docker compose -f flaresolverr.compose.yml up -d                   # Cloudflare 站点需要
```

默认只监听 `127.0.0.1:8080`，通过 SSH 隧道访问：`ssh -L 8080:127.0.0.1:8080 -p <port> <user>@<host>`。

## 注意

- `cf_clearance` 绑定 UA 与出口 IP：在本机浏览器复制的 Cookie 放到服务器上通常无效，Cloudflare 站点请用 FlareSolverr。
- Turnstile / reCAPTCHA 的 token 只能在目标站点页面生成；未配置外部求解服务时会进入人工收件箱并推送通知，需在过期前处理。
- 自动下单可能违反部分商家条款，请自行评估；本程序只生成未支付账单，不会付款。

## License

MIT
