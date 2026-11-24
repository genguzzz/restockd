# restockd HTTP API 契约

所有接口位于 `/api` 下，JSON 编码，时间为 RFC3339 字符串，金额为 `{"cents":18800,"currency":"CNY"}`（分）。
错误统一返回非 2xx 与 `{"error":"人类可读信息"}`：参数校验失败 / 前提不满足（无凭据、站点类型不支持、关注未绑账号、
删除仍被引用的账号、站点未启用）400；记录不存在 404；同一关注 / 商品已有锁单在执行 409；站点侧失败（Provider、网络、登录）502；其余 500。

## 认证

单管理员密码（启动参数 / 环境变量 `RESTOCKD_PASSWORD`）。登录成功后服务端写 HttpOnly Cookie `restockd_session`，
前端无需处理 token；任一接口返回 401 时跳转登录页。

| 方法 | 路径 | 请求 | 响应 |
|---|---|---|---|
| POST | `/api/login` | `{"password":"..."}` | `{"ok":true}` |
| POST | `/api/logout` | - | `{"ok":true}` |
| GET | `/api/me` | - | `{"ok":true,"version":"0.1.0"}`（未登录 401） |

## Provider（适配器类型，只读）

`GET /api/providers`
```json
[
  {
    "kind": "whmcs",
    "name": "WHMCS",
    "description": "通用 WHMCS（含 Lagom 主题）",
    "capabilities": {"catalog": true, "coupon": true, "order": true, "session_check": true, "login": true, "stock_needs_login": false},
    "default_fetch_mode": "flaresolverr",
    "min_interval_sec": 20,
    "default_interval_sec": 60,
    "captcha": ["recaptcha_v2"],
    "config_fields": [
      {"key": "groups", "label": "商品组", "type": "list", "required": true, "placeholder": "cox-isp-vps", "help": "store/<组名> 路径，逗号分隔"},
      {"key": "currency", "label": "货币", "type": "text", "required": false, "default": "CAD"}
    ]
  }
]
```
`config_fields[].type` 取值：`text` | `list`（逗号分隔字符串）| `bool`（"true"/"false"）| `password`。

## Site（被监控站点实例）

`GET /api/sites` → `Site[]`；`POST /api/sites` 新建；`PUT /api/sites/{id}` 更新；`DELETE /api/sites/{id}`；
`POST /api/sites/{id}/poll` 立即轮询一次，返回 `{"ok":true,"products":4}`（站点未启用 400，抓取失败 502）。

```json
{
  "id": "novixlink",
  "kind": "whmcs",
  "name": "NovixLink 诺联",
  "base_url": "https://novixlink.com",
  "config": {"groups": "cox-isp-vps"},
  "interval_sec": 60,
  "fetch_mode": "flaresolverr",
  "egress": ["dmit", "direct"],
  "monitor_account_id": "",
  "hidden_products": ["99"],
  "enabled": true,
  "last_poll_at": "2026-11-24T07:40:00Z",
  "last_error": "",
  "next_poll_at": "2026-11-24T07:41:00Z",
  "consecutive_fail": 0,
  "product_count": 4,
  "in_stock_count": 0,
  "hidden_count": 1
}
```
新建 / 更新请求体为上述对象去掉只读字段（`last_*`、`next_poll_at`、`consecutive_fail`、`*_count`、`hidden_products`）。
`product_count` / `in_stock_count` 不含隐藏商品，`hidden_count` 为隐藏且仍在目录中的商品数。`id` 新建时可空（服务端生成）。
`fetch_mode`：`direct` | `flaresolverr`。`egress`：出口优先级（设置里登记的出口 ID，`direct` 为内置直连），空数组表示使用 `settings.egress.default`。
请求按顺序使用出口：连接失败（出口冷却 2 分钟，对所有站点）或站点 429（按站点 + 出口 + 路径第一段冷却到 Retry-After）时换下一个；
非幂等请求只在连接未建立时换出口；所有出口都在冷却时直接返回限流错误、不发请求。站点显式指定的出口都不可用时报错，不会偷偷回落直连。
FlareSolverr 请求不经过出口层。设置里删除的代理会自动从各站点的 `egress` 中移除。
`monitor_account_id` 必须是本站点的账号。

## Product（商品快照）

`GET /api/products?site_id=&status=&q=&include_hidden=` → `Product[]`（`status` 可选 `in_stock|out_of_stock|preorder|closed|unknown`；`q` 名称模糊匹配；
默认不含隐藏商品，`include_hidden=1` 时包含并带 `"hidden": true`）。
`GET /api/sites/{id}/products` 同上，限定站点。

`POST /api/sites/{site_id}/products/{product_id}/order` 不经关注规则直接锁单 / 试下单 → `Order`（`watch_id` 为空）：
```json
{"account_id": "a1b2", "cycle": "monthly", "options": {"bandwidth_mbps": "200"}, "os": "", "hostname": "", "coupon": "", "gateway": "", "qty": 1, "dry_run": true, "force": false}
```
`account_id` 必填且须属于该站点；`cycle` 缺省 `monthly`，商品有价格时必须是其中之一；`os` / `hostname` / `gateway` 空则用账号默认值。
同一站点 + 商品 + 账号同时只能有一笔在执行（409）。结果与通知规则同关注下单。

**防重复下单**：手动真实下单（直接下单与 `/api/watches/{id}/order`，`dry_run=false`）时，若同一站点 + 商品 + 账号 24 小时内
已有 `placed` 订单则拒绝：409 与 `{"error":"…","order":<已有 Order>}`，不建记录；请求体带 `"force": true` 跳过。
试下单不受限；自动锁单由关注的 `max_orders` 与冷却约束。

```json
{
  "site_id": "usvps24",
  "id": "21",
  "group": "",
  "name": "pinnacle network - 邀请制会员专线 2号服务器",
  "url": "https://usvps24.com/orders/create?product=pinnacle-network-2",
  "specs": {"cpu": "2", "ram_mb": 2048, "disk_gb": 20, "bandwidth_mbps": 200, "traffic_gb": 0, "location": "US-OC-LAKEFOREST", "ip_type": "商业静态IP(非IDC机房)", "extra": {"slug": "pinnacle-network-2"}},
  "prices": [
    {"cycle": "monthly", "amount": {"cents": 18800, "currency": "CNY"}, "setup": {"cents": 0, "currency": "CNY"}, "ref_id": "77"},
    {"cycle": "annually", "amount": {"cents": 180480, "currency": "CNY"}, "setup": {"cents": 0, "currency": "CNY"}, "ref_id": "80"}
  ],
  "options": [
    {"key": "bandwidth_mbps", "label": "带宽", "choices": [{"value": "200", "label": "200 Mbps", "delta_monthly": {"cents": 0, "currency": "CNY"}}, {"value": "300", "label": "300 Mbps", "delta_monthly": {"cents": 8800, "currency": "CNY"}}]}
  ],
  "os_templates": [{"value": "ubuntu-22.04", "label": "Ubuntu 22.04 LTS", "delta_monthly": {"cents": 0, "currency": ""}}],
  "stock": {"status": "in_stock", "qty": 12, "reason": ""},
  "no_discount": false,
  "fetched_at": "2026-11-24T07:40:00Z",
  "watched": true,
  "last_order": {"status": "placed", "created_at": "2026-11-24T07:30:01Z", "pay_url": "https://usvps24.com/invoices/5678", "invoice_id": "5678", "error": ""},
  "last_placed_at": "2026-11-24T07:30:01Z"
}
```
`cycle` 取值：`monthly|quarterly|semiannually|annually|biennially|triennially|onetime`。`stock.qty` 可能缺省（站点未披露）。
**隐藏商品**：`POST /api/sites/{site_id}/products/{product_id}/hide` / `.../unhide` → 更新后的 `Site`（`hidden_products` 只经这两个接口修改，
站点 PUT 不会改动它）。隐藏商品不列出、不计数、不产生事件与通知、不触发自动锁单（关注仍保留）；目录比对照常进行，取消隐藏不会补发积压事件。
手动下单不受隐藏限制。`/api/stats` 的 `products` / `in_stock` 同样不含隐藏商品。

`last_order`：该商品最近一笔订单（任意状态，含试下单与失败，来自关注或直接下单）；`last_placed_at`：最近一次成功锁单时间。没有时两字段都省略。

## Account（托管账号）

`GET /api/accounts?site_id=` → `Account[]`；`POST /api/accounts`；`PUT /api/accounts/{id}`；`DELETE /api/accounts/{id}`。
```json
{
  "id": "a1b2",
  "site_id": "usvps24",
  "label": "主号",
  "user_agent": "Mozilla/5.0 ...",
  "status": "active",
  "identity": "me@example.com",
  "balance": {"cents": 0, "currency": "CNY"},
  "checked_at": "2026-11-24T07:40:00Z",
  "last_error": "",
  "defaults": {"gateway": "qianjimiao", "os": "debian-12", "hostname": "", "ssh_key": ""},
  "created_at": "2026-11-24T07:00:00Z",
  "username": "me@example.com",
  "has_password": true,
  "login_at": "2026-11-24T07:39:00Z",
  "credentials_rejected": false,
  "cookie_names": ["laravel_session", "XSRF-TOKEN", "cf_clearance"]
}
```
`status`：`unknown|active|expired`。Cookie 值永不回传，只回 `cookie_names`。
`DELETE` 时账号仍被关注规则或站点 `monitor_account_id` 引用返回 400（信息里列出引用方）。

- `POST /api/accounts/{id}/cookies` 请求 `{"raw":"<粘贴内容>","user_agent":"可选，cf_clearance 必须配同一 UA"}`
  支持 `a=1; b=2`、`Cookie: ...`、Cookie-Editor JSON 数组、Netscape cookies.txt。响应 `{"ok":true,"cookie_names":[...]}`。
- `POST /api/accounts/{id}/check` 校验会话 → 返回更新后的 `Account`（校验结果在 `status` / `last_error` 里；站点类型不支持校验 400）。
- `POST /api/accounts/{id}/login` 用托管凭据立即登录 → 更新后的 `Account`；登录失败返回 502 与原因（账号状态同步置为 `expired`）。

**账号密码托管**：新建 / 更新请求体可带 `"username"` 与 `"password"`（`username` 省略不修改；`password` 省略或 `"******"` 不修改，空串清除）。
响应只回 `"username"`、`"has_password"`、`"login_at"`，密码永不回传。站点类型声明 `capabilities.login` 时，
会话失效由引擎自动登录并重试一次（下单 / 报价 / 校验在尚无 Cookie 时先登录；轮询公开库存不预先登录）；验证码走设置里的 Solver 链。
站点拒绝密码时推送 `account` 事件并置 `credentials_rejected`，暂停自动登录直到修改凭据或手动调用 `/login`；
其它登录失败（验证码超时、网络等）暂停自动登录 5 分钟（手动 `/login` 不受限）。`account` 事件只在账号变为失效或密码新被拒时推送一次。
修改用户名会丢弃旧 Cookie。登录 / 校验 / 报价的超时为人工验证超时 + 1 分钟（至少 2 分钟）。

新建请求体：`{"site_id":"usvps24","label":"主号","user_agent":"","defaults":{...},"cookies_raw":"可选","username":"可选","password":"可选"}`。

## Watch（关注 / 自动锁单规则）

`GET /api/watches?site_id=` → `Watch[]`；`POST /api/watches`；`PUT /api/watches/{id}`；`DELETE /api/watches/{id}`。
```json
{
  "id": "w1",
  "site_id": "usvps24",
  "product_id": "21",
  "product_name": "pinnacle network - 邀请制会员专线 2号服务器",
  "cycle": "monthly",
  "options": {"bandwidth_mbps": "200", "ip_count": "1"},
  "os": "debian-12",
  "hostname": "",
  "coupon": "bf2026",
  "gateway": "qianjimiao",
  "qty": 1,
  "account_id": "a1b2",
  "auto_order": true,
  "max_price_cents": 20000,
  "max_orders": 1,
  "orders_placed": 0,
  "notify": true,
  "enabled": true,
  "created_at": "2026-11-24T07:00:00Z",
  "last_triggered": "2026-11-24T07:30:00Z"
}
```
- `max_price_cents` 为 0 表示不限；`max_orders` 自动锁单上限（达到后不再自动下单，仅通知）。
- `POST /api/watches/{id}/quote` → `Quote`：`{"subtotal":{...},"discount":{...},"total":{...},"coupon_valid":true,"message":""}`
- `POST /api/watches/{id}/order` 请求 `{"dry_run":true,"force":false}` → `Order`（dry_run 只走到提交前一步；`force` 见上文防重复下单）。
  一旦建立订单记录，失败也返回 200 与 `status: "failed"` 的订单；关注未绑账号 400，已有锁单在执行 409。

## Order（锁单记录）

`GET /api/orders?limit=100` → `Order[]`
```json
{
  "id": "o1",
  "watch_id": "w1",
  "site_id": "usvps24",
  "account_id": "a1b2",
  "product_id": "21",
  "product_name": "pinnacle network 2号",
  "spec": {"product_id": "21", "cycle": "monthly", "options": {}, "os": "debian-12", "coupon": "bf2026", "gateway": "qianjimiao", "qty": 1, "dry_run": false},
  "status": "placed",
  "result": {"order_id": "1234", "invoice_id": "5678", "pay_url": "https://usvps24.com/invoices/5678", "total": {"cents": 15040, "currency": "CNY"}, "message": ""},
  "error": "",
  "created_at": "2026-11-24T07:30:01Z",
  "duration_ms": 842
}
```
`status`：`pending|placed|dry_run|failed`。每笔订单都会落到 `placed|dry_run|failed` 之一并记 `order` 事件；
试下单（成败）只记事件不推送通知。订单记录保留 90 天。

## Event（事件流）

`GET /api/events?limit=200&site_id=` → `Event[]`
```json
{"id": 42, "type": "restock", "site_id": "novixlink", "product_id": "41", "title": "LAS-COX Lite 补货 (3 台)", "detail": "", "created_at": "2026-11-24T07:30:00Z"}
```
`type`：`restock|sold_out|price_change|new_product|order|challenge|poll_error|account`（`account`：会话失效无法自动恢复、自动登录被拒）。

### 实时推送（SSE）

`GET /api/stream`，`text/event-stream`，每条 `event: <name>` + `data: <json>`：
- `event: event` → `Event`（新事件）
- `event: site` → `Site`（轮询状态变化）
- `event: challenge` → `Pending`（新挂起挑战）
- `event: challenge_done` → `{"id":"..."}`
- `event: order` → `Order`
- `event: account` → `Account`（任何账号变化：保存、Cookie、登录、校验、状态、会话滚动刷新）
每 25 秒有 `: ping` 注释行保活。

## Challenge（人工验证收件箱）

`GET /api/challenges` → `Pending[]`
```json
{
  "id": "9f1c...",
  "challenge": {"kind": "turnstile", "site_key": "0x4AAA...", "page_url": "https://usvps24.com/cart", "action": "", "site_id": "usvps24", "account_id": "a1b2", "purpose": "usvps24 锁单 pinnacle-network-2"},
  "created_at": "2026-11-24T07:30:00Z",
  "expires_at": "2026-11-24T07:35:00Z"
}
```
`kind`：`turnstile|recaptcha_v2|recaptcha_v3|hcaptcha`（Cloudflare 质询由出口层的 FlareSolverr 回退处理，不进收件箱）。
- `POST /api/challenges/{id}/solve` 请求 `{"token":"可选","cookies_raw":"可选","user_agent":"可选"}`（至少其一）→ `{"ok":true}`
- `POST /api/challenges/{id}/cancel` → `{"ok":true}`

## Settings

`GET /api/settings` / `PUT /api/settings`（整体覆盖；密码类字段回传 `"******"`，PUT 时原样传回 `"******"` 表示不修改）。
```json
{
  "telegram": {"enabled": true, "bot_token": "******", "chat_id": "123456", "api_base": ""},
  "email": {"enabled": false, "host": "smtp.gmail.com", "port": 587, "username": "", "password": "******", "from": "", "to": "", "starttls": true},
  "bark": {"enabled": true, "device_urls": "******", "group": "restockd", "sound": "", "icon": ""},
  "solver": {"flaresolverr_url": "http://127.0.0.1:8191", "external_url": "", "external_token": "******", "manual_timeout_sec": 300},
  "polling": {"idle_interval_sec": 3600, "viewer_interval_sec": 300},
  "egress": {"proxies": [{"id": "dmit", "name": "dmit LA", "url": "socks5://127.0.0.1:10820", "enabled": true}], "default": ["dmit", "direct"]},
  "notify_events": ["restock", "order", "challenge", "account"]
}
```
列表字段（`egress.proxies`、`egress.default`、`notify_events`）始终为数组，不会是 `null`。
- `polling`：没有启用中关注规则的站点自动降频——无人访问按 `idle_interval_sec`，有人访问（SSE 连着或最近
  `viewer_interval_sec` 内有已登录请求）按 `viewer_interval_sec`，访问开始时立即刷新一次；都不低于站点自身间隔。
  有关注的站点始终按站点 `interval_sec`。增删关注规则会立即重排。
- `bark.device_urls`：Bark 设备地址 `https://<server>/<device key>`，逗号 / 换行分隔多台；属于密钥，读取时回传 `"******"`。
  补货 / 锁单 / 人工验证以 `timeSensitive` 级别推送，其余事件为 `active`。
- `egress.proxies[].url` 中的密码读取时为 `******`，原样传回表示密码不修改（主机、端口、用户名以传回的为准）；
  `id` 必填、不重复且不能为 `direct`（否则 400）。
- `POST /api/egress/{id}/test`（`id` 可为 `direct`）→ `{"ok":true,"ip":"…","loc":"US","colo":"LAX","ms":420}`，经该出口访问 Cloudflare trace；
  请求失败为 `{"ok":false,"error":"…"}`，出口不存在或已停用 400。
- `POST /api/settings/test-notify` → `{"ok":true,"results":{"telegram":"ok","email":"disabled","bark":"not configured"}}`
  （每个通道：`ok` / 错误信息 / `disabled` 未启用 / `not configured` 已启用但缺少必填项）

## Stats（首页）

`GET /api/stats`
```json
{"sites": 2, "products": 15, "in_stock": 1, "watches": 3, "auto_watches": 2, "orders_24h": 0, "pending_challenges": 0, "events_24h": 12}
```
