# MantisOps 前端功能模块说明

> 技术栈：React 19 + TypeScript + TailwindCSS v4 + Recharts + Zustand
> 图标：Material Symbols Outlined
> 认证：JWT 多用户鉴权（admin/operator/viewer 三级角色）

---

## 一、页面总览

| 页面 | 路由 | 菜单名称 | 权限 | 定位 |
|------|------|---------|------|------|
| 登录 | `/login` | — | 公开 | JWT 登录页面，未认证自动跳转 |
| 强制改密 | `/change-password` | — | 公开 | 首次登录强制修改密码 |
| 仪表盘 | `/` | 仪表盘 | all | 全局总览：统计卡片 + 告警/RDS/到期摘要 + 分组服务器列表 + 端口摘要 |
| 服务器列表 | `/servers` | 服务器 | all | 卡片/表格双视图，自定义分组 + 排序 |
| 服务器详情 | `/servers/:id` | — | all | 实时概览 + Docker 容器 + 端口检测 + 历史趋势 + Agent 配置 |
| NAS 存储 | `/nas` | NAS 存储 | all | NAS 设备列表 + 实时指标 |
| NAS 详情 | `/nas/:id` | — | all | RAID/S.M.A.R.T./存储卷/UPS + 历史趋势 |
| 数据库监控 | `/databases` | 数据库 | all | RDS 实例列表（按云账号分组）+ 实时指标 |
| 数据库详情 | `/databases/:id` | — | all | RDS 实例指标瓦片 + 历史趋势图 |
| 托管业务 | `/assets` | 托管业务 | all | 按服务器分组的资产表格，CRUD + 技术栈标签 |
| 告警中心 | `/alerts` | 告警中心 | all | 告警事件 + 规则配置 + 通知渠道 |
| 网络拓扑 | `/network` | 网络拓扑 | all（扫描 admin） | D3.js 拓扑图 + 设备列表 + 网段概览 + ICMP/SNMP 扫描 |
| 日志中心 | `/logs` | 日志中心 | all | 操作审计 Tab + 运行日志 Tab（查询/实时） |
| AI 报告 | `/ai-reports` | AI 报告 | all | AI 运维分析报告生成 + 模板编辑 + 报告管理 |
| 资源到期 | `/billing` | 资源到期 | all | ECS/RDS/SSL 到期提醒 |
| 系统设置 | `/system` | 系统设置 | admin | 平台配置 + 接入管理 + NAS/托管服务器/云账号管理 + AI 配置 |
| 用户管理 | `/users` | 用户管理 | admin | 用户 CRUD + 角色 + 资源权限配置 |
| 系统信息 | `/settings` | 系统信息 | all | 系统版本 + 已注册 Agent 列表（Docker/GPU 状态） |

---

## 二、认证系统

| 功能 | 说明 |
|------|------|
| 登录页 | 居中玻璃卡片，用户名 + 密码输入，渐变登录按钮 |
| JWT 鉴权 | 登录成功返回 JWT token（7 天有效期），持久化到 localStorage |
| 路由守卫 | `RequireAuth`（未登录跳转 `/login`）+ `RequireChangePwd`（强制改密）+ `RequireAdmin`（非 admin 跳转首页） |
| Axios 拦截器 | 请求自动附加 `Authorization: Bearer` 头，401 响应自动跳转登录页 |
| 用户菜单 | 右上角头像图标，点击展开下拉菜单显示用户名 + 退出登录 |
| 主题切换 | 右上角太阳/月亮图标，点击切换深色/浅色主题 |

**API：**
- `POST /api/v1/auth/login` — 登录
- `GET /api/v1/auth/me` — 获取当前用户

---

## 三、各页面功能详解

### 3.1 仪表盘 (`/`)

全局总览中心，整合告警、资源到期、RDS、服务器分组等信息。

| 模块 | 位置 | 内容 |
|------|------|------|
| 统计卡片行 | 顶部 6 列 | 服务器在线数、运行中容器数、端口探测正常数、平均 CPU、**告警中（firing_unsilenced）**、**即将到期（30天内）** |
| 摘要区域 | 统计卡片下方 3 列 | **未处理告警**（最近 5 条未静默 firing 事件）、**数据库状态**（6 个 RDS 迷你 CPU/内存条）、**30天内到期**（ECS/RDS/SSL 到期资源列表） |
| 服务器状态列表 | 左列(7/12) | **按分组折叠展示**：每组组头（名称 + 在线/总数 + 折叠箭头），组内每台服务器一行（图标盒 + 主机名 + IP + CPU/MEM/DISK 进度条 + 网络速率） |
| 端口监控摘要 | 右列上(5/12) | 所有探测规则实时状态，15 秒自动刷新 |
| 资源使用排行 | 右列下 | Top 5 服务器，按 CPU 排序 |

**数据来源（并行 5 个 API + WebSocket）：**
- 服务器 + 分组 + 指标快照：`GET /api/v1/dashboard`（含 `groups` 和 `metrics` 字段）
- 告警统计：`GET /api/v1/alerts/stats`
- 告警事件：`GET /api/v1/alerts/events?status=firing&silenced=false&limit=5`
- RDS 状态：`GET /api/v1/databases`
- 到期信息：`GET /api/v1/billing`
- 实时更新：WebSocket（服务器指标 + `alert`/`alert_resolved`/`alert_acked` 消息）

---

### 3.2 服务器列表 (`/servers`)

所有服务器的详细视图，支持两种展示模式和自定义分组管理。

**分组展示（卡片和表格视图均支持）：**
- 按自定义分组折叠展示，组头：分组名 + (在线/总数) + 折叠箭头
- 未分组服务器归入"未分组"尾部
- 默认全部展开

**卡片视图（默认）：**
- 响应式网格（1-4 列自适应）
- 每张卡片：图标盒 + 主机名 + 状态标签 + IP + 硬件摘要标签 + 三条进度条 + 网络速率 + 分组选择器

**表格视图：**
- 紧凑行式：状态灯、主机名（可点击）、IP、系统、CPU%、内存%、磁盘%、流量、容器数、分组选择
- CPU/内存/磁盘百分比带颜色编码（绿/黄/红）

**分组管理：**
- 顶部"管理分组"按钮，点击展开管理面板
- 面板内：已有分组列表（可删除）+ 新建分组输入框
- 每个服务器卡片/表格行内有分组下拉选择器，切换即时生效

**底部统计栏：** 服务器总数/在线数、平均 CPU、总流量、运行容器数

**视图切换：** 分段控件（卡片/表格）

**分组 API：**
- `GET /api/v1/groups` — 列出所有分组（含 server_count）
- `POST /api/v1/groups` — 创建分组
- `PUT /api/v1/groups/:id` — 更新分组
- `DELETE /api/v1/groups/:id` — 删除分组（组内服务器解绑）
- `PUT /api/v1/servers/:id/group` — 设置服务器所属分组

---

### 3.3 服务器详情 (`/servers/:id`)

单台服务器的全维度监控视图。

| 模块 | 内容 |
|------|------|
| 头部 | 返回按钮 + 「服务器详情」标签 + 服务器名称（可编辑，点击铅笔图标） + 运行中/离线状态徽章 |
| Bento Grid 左列(1/3) | 服务器基本信息：OS、内核、CPU 型号、内存总量、磁盘总量、GPU（如有）、IP、心跳、Agent 版本、架构 |
| Bento Grid 右列(2/3) | **实时概览**：3x3 网格卡片 — CPU（含 load 1/5/15）、内存（含 swap）、磁盘、网络入站、网络出站、容器数。有 GPU 时追加 GPU 使用率/显存/温度 |
| 运行业务 | 右列下方，展示该服务器部署的所有业务项目：名称、描述、技术栈标签、路径、端口 |
| Docker 容器表格 | 容器名、状态（发光点）、CPU%、内存、镜像 |
| **端口检测** | 该服务器的探测规则卡片网格：服务名、协议标签、地址、状态灯+延迟、SSL 到期徽章、来源标签（手动/自动）。支持添加/删除规则（operator+）。10 秒轮询状态 |
| 历史趋势 | 时间范围切换（1h/6h/24h/7d）+ 刷新按钮。2 列网格展示 6 个历史图表：CPU 使用率、系统负载、内存使用率、磁盘使用率、网络流量合计、网络分网卡。有 GPU 时追加 3 个：GPU 使用率、GPU 显存、GPU 温度 |

**服务器名称编辑：** 点击标题旁铅笔图标，切换为输入框，Enter 保存 / Esc 取消。

**历史趋势特性：**
- 数据来自 VictoriaMetrics，通过 Nginx 反代 `/vm/api/v1/query_range`
- 父级统一计算时间窗口，所有图表 X 轴对齐
- AbortController 处理请求竞态（快速切换不会数据错乱）
- 自适应数值精度（小数值自动增加小数位数）
- 三态：加载中旋转动画 / 错误重试按钮 / 无数据提示

**Agent 配置对话框：**
- Docker 容器监控开关、GPU 监控开关、**端口自动检测开关**（默认关闭）
- 保存调用 `PUT /api/v1/servers/:id/config`（含 `probe_auto_scan` 字段）

**数据来源：**
- 基本信息：`GET /api/v1/servers/:id`
- 实时指标：WebSocket
- 运行业务：`GET /api/v1/assets`（按 server_id 过滤）
- 端口检测规则：`GET /api/v1/probes?server_id={id}`
- 端口检测状态：`GET /api/v1/probes/status?server_id={id}`（10 秒轮询）
- 历史趋势：`/vm/api/v1/query_range`（Nginx 代理 VictoriaMetrics）
- 名称修改：`PUT /api/v1/servers/:id/name`

---

### 3.4 数据库监控 (`/databases`)

RDS 云数据库实例监控。

| 功能 | 说明 |
|------|------|
| 实例列表 | 卡片式展示数据库实例，显示名称、类型（MySQL/PostgreSQL）、实时 CPU/内存/磁盘/连接数 |
| 实例详情 | `/databases/:id`，实时指标瓦片（8-10 个指标）+ 历史趋势图表，支持时间范围切换 |

**数据来源：**
- `GET /api/v1/databases` — 实例列表
- `GET /api/v1/databases/:id` — 实例详情

---

### 3.5 容器管理 (`/containers`)

全局 Docker 容器聚合列表，跨服务器查看所有容器。

| 功能 | 说明 |
|------|------|
| 统计卡片行 | 4 张卡片：总容器数、运行中、已停止、宿主机数 |
| 筛选 Tab | 全部 / 运行中 / 已停止，每项显示数量 |
| 搜索 | 按容器名、镜像、服务器名模糊搜索 |
| 容器表格 | 状态标签、容器名（含 ID）、镜像、宿主机（可点击跳转服务器详情）、CPU%、内存（使用/限制）、端口映射、运行状态 |
| 空状态 | 无容器时提示开启 Agent Docker 采集 |

**数据来源：**
- 服务器列表：`GET /api/v1/dashboard`
- 容器数据：WebSocket 实时推送 `MetricsPayload.containers`，聚合所有服务器

---

### 3.6 本地业务 (`/assets`)

服务器上部署的项目和服务信息管理。

| 功能 | 说明 |
|------|------|
| 按服务器分组 | 每组：服务器图标盒 + 名称 + IP 徽章 + 硬件摘要 |
| 资产表格 | 项目名称（含描述）、技术栈（彩色标签拆分）、路径（mono 字体）、端口 |
| 添加资产 | 渐变按钮展开玻璃卡片表单 |
| 删除 | 行悬停显示删除按钮，有确认弹窗 |
| 底部统计 | 活跃资产数、总计、服务器数 |

**数据来源：**
- `GET /api/v1/assets`、`POST`（创建）、`PUT`（更新）、`DELETE`（删除）

---

### 3.7 告警中心 (`/alerts`)

告警通知系统的管理中心，包含三个 Tab。

**统计卡片行：** 4 张卡片 — 当前触发中、今日触发、今日恢复、今日确认

**Tab 1 — 告警事件：**

| 功能 | 说明 |
|------|------|
| 状态筛选 | 全部 / 触发中 / 已恢复 / 已静默 |
| 事件表格 | 级别 emoji、告警名称（rule_name 快照）、目标（target_label 快照）、触发值、状态、触发时间、操作 |
| firing 行 | 左红边框 + 浅红背景，"确认"按钮 |
| firing + silenced 行 | 左橙边框，显示确认人和时间 |
| resolved 行 | 显示恢复时间 + 恢复原因（自动恢复/目标消失/规则禁用/规则删除） |
| 通知投递详情 | 点击行展开，查看各渠道投递状态（sent/failed/pending） |
| 自动刷新 | 15 秒轮询 |

**Tab 2 — 告警规则：**

| 功能 | 说明 |
|------|------|
| 规则列表 | 规则名、类型、目标、条件、连续次数、级别、启用开关、删除 |
| 添加规则 | 表单：名称、类型（按「服务器/探针」「数据库」「NAS」分组）、目标、运算符、阈值、连续次数、级别 |
| 规则类型 | 服务器/探针：server_offline、probe_down、cpu、memory、disk、container、gpu_temp、gpu_memory、network_rx、network_tx；数据库：db_cpu、db_memory、db_disk、db_connection、db_iops（RDS 使用率，目标 `db:<host_id>`）；NAS：nas_offline、nas_raid_degraded、nas_disk_smart、nas_disk_temperature、nas_volume_usage、nas_ups_battery |
| 动态生效 | 启用/禁用/删除规则时自动处理关联的 firing 事件 |

**Tab 3 — 通知渠道：**

| 功能 | 说明 |
|------|------|
| 渠道列表 | 卡片式：渠道名、类型图标、URL 脱敏、启用开关 |
| 添加渠道 | 表单：名称、类型（钉钉/Webhook）、URL、密钥 |
| 测试通知 | 每张卡片有"测试"按钮，发送测试消息验证配置 |

**告警引擎特性：**
- 后端 30 秒轮询评估所有 enabled 规则
- 连续 N 次超阈值才触发（防抖动），连续 N 次正常才恢复（对称）
- 同一规则未处理前不重复发送
- 手动确认 = 静默通知（不改变 firing 状态，等待自动恢复）
- 事件 + 通知记录在同一 SQLite 事务中原子落库
- 通知异步发送，带认领机制防重复，失败自动重试（最多 3 次）
- 恢复通知只发给触发时绑定的渠道集合
- 目标消失（容器删除/磁盘卸载/主机移除）自动 resolve，不发外部通知
- 指标新鲜度检查（>120s 的陈旧数据跳过，防误报）

**数据来源：**
- 规则：`GET /api/v1/alerts/rules`、`POST`、`PUT`、`DELETE`
- 事件：`GET /api/v1/alerts/events`（支持 status/silenced/since/until/limit/offset 分页）
- 统计：`GET /api/v1/alerts/stats`
- 确认：`PUT /api/v1/alerts/events/:id/ack`
- 投递详情：`GET /api/v1/alerts/events/:id/notifications`
- 渠道：`GET /api/v1/alerts/channels`、`POST`、`PUT`、`DELETE`
- 测试：`POST /api/v1/alerts/channels/:id/test`
- 实时推送：WebSocket `alert`/`alert_resolved`/`alert_acked` 消息

---

### 3.8 AI 报告 (`/ai-reports`)

AI 运维分析报告的生成、管理和模板编辑。

**报告列表页：**

| 功能 | 说明 |
|------|------|
| 报告卡片网格 | 按类型标签 + 状态徽章 + 摘要 + 生成信息（provider/tokens/耗时） |
| 类型筛选 | Tab 切换：全部 / 日报 / 周报 / 月报 / 季度 / 年度 |
| 生成报告 | 对话框选择报告类型，后台异步生成 |
| 生成进度 | 生成中卡片显示旋转动画 + 实时计时 |
| 删除报告 | 卡片悬停显示删除按钮，点击弹出确认对话框 |
| 编辑模板 | 模态对话框编辑各类型报告的 AI 提示词模板 |
| WebSocket 更新 | 报告完成/失败时自动刷新列表 |

**报告详情页 (`/ai-reports/:id`)：**

| 功能 | 说明 |
|------|------|
| Markdown 渲染 | ReactMarkdown + remarkGfm 渲染报告内容 |
| 元信息栏 | Provider、模型、Token 数、耗时、触发方式 |
| 导出 Markdown | 下载报告为 .md 文件 |
| 失败状态 | 显示错误信息 |

**模板编辑对话框：**

| 功能 | 说明 |
|------|------|
| 5 种类型 Tab | 日报 / 周报 / 月报 / 季度 / 年度 |
| 默认模板预填 | 打开时从后端加载内置默认模板填入编辑区 |
| 自定义覆盖 | 编辑后保存，内容与默认一致时自动使用默认 |
| 恢复默认 | 一键重置为系统内置模板 |

**数据来源：**
- 报告列表：`GET /api/v1/ai/reports`
- 报告详情：`GET /api/v1/ai/reports/:id`
- 生成报告：`POST /api/v1/ai/reports/generate`
- 删除报告：`DELETE /api/v1/ai/reports/:id`
- 导出：`GET /api/v1/ai/reports/:id/download`
- 模板读取：`GET /api/v1/ai/prompts`（返回 custom + defaults）
- 模板保存：`PUT /api/v1/ai/prompts`
- 实时更新：WebSocket `ai_report_completed` 事件

---

### 3.9 网络拓扑 (`/network`)

网络设备发现、拓扑可视化与连通性监控。

**三个 Tab：**

**Tab 1 — 拓扑图**

| 功能 | 说明 |
|------|------|
| D3.js 力导向图 | 节点按 device_type 显示不同图标（switch/router/ap/firewall/server/unknown） |
| 状态颜色 | online 绿色、offline 灰色 |
| 服务器标识 | 已关联 MantisOps 服务器的节点半径更大 |
| 交互 | 拖拽节点、缩放平移、悬停信息卡片（IP/厂商/型号/状态） |
| LLDP 链路 | 连线来自 SNMP LLDP/CDP 邻居发现，离线连接虚线 |

**Tab 2 — 设备列表**

| 功能 | 说明 |
|------|------|
| 表格 | 状态灯、IP、MAC、厂商、类型（可编辑下拉）、型号、SNMP、网段、最后在线 |
| 筛选 | 按网段/类型/状态 |
| 搜索 | IP/MAC/厂商/型号 |
| 操作 | admin 可修正设备类型、删除设备 |

**Tab 3 — 网段概览**

| 功能 | 说明 |
|------|------|
| 网段卡片 | CIDR、网关、设备数/在线数、在线率进度条 |
| 颜色编码 | >80% 绿、>50% 黄、≤50% 红 |

**扫描管理（admin only）：**
- 「扫描网络」按钮 → 弹窗输入 CIDR（逗号分隔，限 /24 或更小）
- 显示预估耗时，二次确认
- 扫描中显示进度条 + 取消按钮

**数据来源：**
- `POST /api/v1/network/scan` — 触发扫描
- `GET /api/v1/network/scan/status` — 扫描状态
- `DELETE /api/v1/network/scan` — 取消扫描
- `GET /api/v1/network/devices` — 设备列表
- `GET /api/v1/network/topology` — 拓扑图数据（nodes + edges）
- `GET /api/v1/network/subnets` — 网段列表
- WebSocket：`network_scan_progress`、`network_scan_subnet_done`、`network_scan_job_done`、`network_device_status`

---

### 3.10 资源到期 (`/billing`)

ECS / RDS / SSL 证书到期提醒。

| 功能 | 说明 |
|------|------|
| 统计卡片行 | 5 张卡片：紧急(30天内)、预警(60天内)、ECS 实例数、RDS 实例数、SSL 证书数 |
| 紧急告警横幅 | 30 天内到期资源高亮提醒（红色） |
| **分类筛选 Tab** | 全部 / ECS / RDS / SSL，每项显示数量，点击切换过滤 |
| 到期表格 | 类型标签（ECS绿/RDS青/SSL黄）、名称（含 ID）、所属账号、规格/引擎、计费方式、到期日期、剩余天数 |
| 排序 | 有效资源按天数升序在前，已过期按天数降序在后 |
| 颜色编码 | ≤30天红色、30-60天黄色、>60天绿色、已过期红色负数 |

**SSL 证书数据来源：**
- 阿里云 CAS API（`ListUserCertificateOrder`），查询 CPACK + CERT 两种类型
- 按域名+到期日去重
- 过期超过 90 天的自动过滤不展示
- 与 ECS/RDS 一起在 BillingHandler 每小时缓存刷新

**数据来源：**
- `GET /api/v1/billing` — 返回 ECS + RDS + SSL 统一列表

---

### 3.11 系统信息 (`/settings`)

系统版本和 Agent 管理。

| 模块 | 内容 |
|------|------|
| 系统信息条 | 前端版本号、Agent 在线数/总数 |
| 已注册 Agent | 表格：主机名、Host ID（mono）、Agent 版本、最后心跳、在线/离线状态（发光点 + 文字标签） |

---

### 3.12 用户管理 (`/users`，仅 admin 可见)

多用户账号管理，admin 角色专属页面。

| 功能 | 说明 |
|------|------|
| 用户列表 | 表格：用户名、显示名、角色标签（admin 绿/operator 蓝/viewer 灰）、状态开关、待改密标记、操作列 |
| 创建用户 | 对话框：用户名、初始密码、显示名、角色选择。创建后 must_change_pwd=1 |
| 编辑用户 | 对话框：显示名、角色、启用/禁用。编辑自己时操作置灰 |
| 重置密码 | 确认对话框：输入新初始密码，重置后用户需改密 |
| 删除用户 | 确认对话框，系统最后一个 admin 不可删除，不可删除自己 |
| 权限配置 | 跳转到 `/users/:id/permissions` 权限树页面（仅非 admin 用户显示按钮） |

**数据来源：**
- `GET /api/v1/users` — 用户列表
- `POST /api/v1/users` — 创建用户
- `PUT /api/v1/users/:id` — 编辑用户
- `DELETE /api/v1/users/:id` — 删除用户
- `PUT /api/v1/users/:id/reset-pwd` — 重置密码

---

### 3.13 权限配置 (`/users/:id/permissions`，仅 admin 可见)

树形资源权限配置页面。

| 功能 | 说明 |
|------|------|
| 资源树 | 左栏：服务器分组（含子服务器）+ 数据库 + 探测规则三大类，checkbox 勾选 |
| 分组联动 | 勾选分组自动包含组内所有服务器（子节点置灰不可单独取消） |
| 跨组追加 | 未分组或其他组的服务器可单独勾选 |
| 已选摘要 | 右栏：已选资源列表，类型标签 + 名称，hover 可移除 |
| 保存 | 全量覆盖写入，后端自动去重 |

**数据来源：**
- `GET /api/v1/users/:id/permissions` — 当前权限
- `PUT /api/v1/users/:id/permissions` — 设置权限
- `GET /api/v1/groups`、`GET /api/v1/servers`、`GET /api/v1/databases`、`GET /api/v1/probes` — 资源列表

---

### 3.14 强制改密 (`/change-password`)

管理员创建用户后首次登录的强制改密页面。

| 功能 | 说明 |
|------|------|
| 表单 | 初始密码 + 新密码 + 确认新密码 |
| 提示 | "管理员已为您设置初始密码，请输入初始密码后设置新密码" |
| 流程 | 调用 `PUT /api/v1/auth/password`，成功后用新 token 替换本地存储，跳转首页 |

**路由守卫：** `RequireChangePwd` 组件检测 `mustChangePwd=true` 时强制跳转此页面，拦截其他路由。

---

## 四、布局与导航

### 顶部导航栏（固定，响应式 left-0 md:left-[250px]）

| 元素 | 位置 | 功能 |
|------|------|------|
| 汉堡菜单 | 左（移动端） | 切换侧边栏 |
| 搜索框 | 左（桌面端） | 仅在服务器/NAS/数据库页面显示，按页面类型搜索对应资源，下拉结果可点击跳转 |
| 刷新按钮 | 右 | sync 图标 |
| 通知铃铛 | 右 | NotificationBell 组件：红色徽章显示 firing_unsilenced 数，点击下拉显示最近 10 条告警，"查看全部"跳转 /alerts |
| 用户菜单 | 右 | 头像 + 用户名，下拉：用户信息 + 退出登录 |

### 侧边栏（固定，移动端可收起）

| 菜单项 | 图标 | 路由 | 可见角色 |
|--------|------|------|---------|
| 仪表盘 | dashboard | `/` | 所有 |
| 服务器 | dns | `/servers` | 所有 |
| NAS 存储 | hard_drive | `/nas` | 所有 |
| 数据库 | database | `/databases` | 所有 |
| 托管业务 | inventory_2 | `/assets` | 所有 |
| 告警中心 | notifications_active | `/alerts` | 所有 |
| 网络拓扑 | device_hub | `/network` | 所有 |
| 日志中心 | article | `/logs` | 所有 |
| AI 报告 | analytics | `/ai-reports` | 所有 |
| 资源到期 | event_upcoming | `/billing` | 所有 |
| 系统设置 | admin_panel_settings | `/system` | **admin** |
| 用户管理 | group | `/users` | **admin** |
| 系统信息 | info | `/settings` | 所有 |

激活项样式：绿色文字高亮

---

## 五、通用组件

| 组件 | 文件 | 说明 |
|------|------|------|
| ProgressBar | `components/ProgressBar.tsx` | 进度条，色调分层（<60% 绿、<80% 黄、>=80% 红），支持 sm/md 尺寸 |
| StatusBadge | `components/StatusBadge.tsx` | 发光脉冲状态指示器，支持 online/offline/up/down + 文字标签 |
| ServerCard | `components/ServerCard.tsx` | 服务器卡片，含硬件标签、进度条、网络速率、容器数、GPU 徽章、分组选择器 |
| HistoryChart | `components/HistoryChart.tsx` | 历史趋势图表，查询 VictoriaMetrics，支持多线叠加、AbortController 竞态处理、三态 UI、自适应精度 |
| ThemeToggle | `components/ThemeToggle.tsx` | 深色/浅色分段控件 |
| NotificationBell | `components/NotificationBell.tsx` | 顶栏告警铃铛：红色徽章 + 下拉面板 + 新告警脉冲动画 |
| Sidebar | `components/Layout/Sidebar.tsx` | 左侧导航栏（深色），9 个导航项，移动端可收起 |
| MainLayout | `components/Layout/MainLayout.tsx` | 页面骨架：顶栏（Logo + 刷新 + 通知铃铛 + 主题切换 + 用户菜单）+ 侧边栏 + 内容区 |

---

## 六、实时数据机制

| 机制 | 说明 |
|------|------|
| WebSocket | 全局单连接（MainLayout 层），`/ws?token=<jwt>` 端点（鉴权），自动断线 3 秒重连，未登录不连接 |
| 消息格式 | `{"type": "metrics", "host_id": "xxx", "data": MetricsPayload}` |
| 告警消息 | `{"type": "alert", "data": AlertEvent}`、`{"type": "alert_resolved", "data": {"id": N}}`、`{"type": "alert_acked", "data": {"id": N, "acked_by": "admin"}}` |
| 状态管理 | Zustand serverStore（服务器/指标）+ alertStore（告警事件/统计） |
| 认证管理 | Zustand authStore 维护 `token` 和 `username`，持久化到 localStorage |
| 端口探测轮询 | 仪表盘 15 秒、端口监控页 10 秒轮询 `GET /api/v1/probes/status` |
| 历史数据 | 前端通过 `/vm/api/v1/query_range` 查询 VictoriaMetrics（Nginx 精确代理） |

---

## 七、设计系统 — Kinetic Observatory

### 色彩体系（深色主题 / 浅色主题）

通过 TailwindCSS v4 `@theme` 定义设计令牌，`<html class="dark">` 切换。

**深色（默认）：**
- 背景层级：`#0b1326` → `#131b2e` → `#171f33` → `#222a3d` → `#2d3449`
- 主色：`#a4c9ff`（Primary）、`#4edea3`（Tertiary/成功）、`#ffb4ab`（Error）、`#fbbf24`（Warning）
- 文字：`#dae2fd`（主）、`#c1c7d3`（副）

**浅色：**
- 完整浅色 token 覆盖，通过 `html:not(.dark)` CSS 规则

### 核心视觉特性
- 玻璃拟态（`glass-card`：rgba 背景 + backdrop-blur）
- 发光脉冲状态指示器（`pulse-glow-success/error`）
- 悬停卡片发光（`glow-card`）
- 网格背景（`bg-grid-pattern`）
- 无边框设计（色调层级区分层次）
- 渐变按钮（`bg-gradient-to-br from-primary to-primary-container`）

---

## 八、安全

| 层面 | 机制 |
|------|------|
| REST API | JWT Bearer token 鉴权（`Authorization: Bearer <token>`），所有 `/api/v1/*` 路由受 JWTMiddleware 保护 |
| 多用户认证 | bcrypt 密码哈希（cost=10），JWT payload 含 user_id/role/token_version/must_change_pwd |
| 三级角色 | `admin` > `operator` > `viewer`，RequireRole 中间件，路由分组（viewer/operator/admin） |
| 资源级权限 | PermissionCache 按 user_permissions 表 + 分组展开计算可见资源集合，所有 List 接口过滤 |
| 即时踢下线 | JWT token_version 机制，角色变更/禁用/改密时版本号递增，旧 token 立即失效 |
| WebSocket | 连接时通过 query param `?token=<jwt>` 鉴权，Hub 按连接 PermissionSet 过滤推送 |
| gRPC Agent | PSK（Pre-Shared Key）拦截器，Agent 连接时携带 `Authorization: Bearer <psk_token>` |
| 启动校验 | 服务启动时校验 jwt_secret、psk_token 非空；首次启动从 server.yaml 迁移初始 admin |
| 配置文件 | 仓库中 `configs/server.yaml` 为空值模板，实际密钥只存在于部署服务器的配置中，不入版本控制 |
| Nginx | `/vm/api/v1/query_range` 仅允许 GET 请求（`limit_except GET { deny all; }`），限制 VictoriaMetrics 访问范围 |

---

## 九、架构

```
浏览器 → Nginx
            ├── /                          → 静态文件 (web/dist/)
            ├── /api/*                     → Go Server (HTTP)
            ├── /ws                        → Go Server (WebSocket)
            └── /vm/api/v1/query_range     → VictoriaMetrics
```
