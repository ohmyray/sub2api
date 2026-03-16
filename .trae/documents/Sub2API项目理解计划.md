# Sub2API 项目理解计划

## 一、项目概述

**Sub2API** 是一个 **AI API 网关平台**，用于分发和管理 AI 产品订阅（如 Claude Code $200/月）的 API 配额。

### 核心功能
- 用户通过平台生成的 API Key 调用上游 AI 服务
- 平台负责：鉴权、计费、负载均衡、请求转发

---

## 二、技术栈

| 组件 | 技术 |
|------|------|
| 后端 | Go 1.25.7, Gin Web 框架, Ent ORM |
| 前端 | Vue 3.4+, Vite 5+, TailwindCSS |
| 数据库 | PostgreSQL 15+ |
| 缓存/队列 | Redis 7+ |
| 代码生成 | Ent (ORM), Wire (依赖注入) |

---

## 三、项目目录结构

```
sub2api/
├── backend/                     # Go 后端服务
│   ├── cmd/
│   │   ├── server/              # 主程序入口
│   │   └── jwtgen/              # JWT 生成工具
│   ├── ent/                     # Ent ORM 生成的代码
│   │   └── schema/              # 数据库 schema 定义
│   ├── internal/
│   │   ├── config/              # 配置管理
│   │   ├── domain/              # 领域模型/常量
│   │   ├── handler/              # HTTP 请求处理
│   │   │   ├── admin/           # 管理后台处理器
│   │   │   ├── dto/             # 数据传输对象
│   │   │   └── *.go             # 各类业务处理器
│   │   ├── middleware/          # 中间件（限流等）
│   │   ├── model/               # 数据模型
│   │   ├── pkg/                 # 公共工具包
│   │   │   ├── errors/          # 错误处理
│   │   │   ├── logger/          # 日志
│   │   │   ├── oauth/           # OAuth 相关
│   │   │   ├── proxyurl/        # 代理 URL 解析
│   │   │   └── ...
│   │   ├── repository/          # 数据访问层
│   │   ├── server/              # HTTP 服务器
│   │   │   ├── middleware/      # 服务端中间件
│   │   │   └── routes/          # 路由定义
│   │   ├── service/             # 业务逻辑层
│   │   │   ├── account_*.go     # 账号管理
│   │   │   ├── gateway_*.go     # API 网关核心
│   │   │   ├── auth_*.go        # 认证服务
│   │   │   ├── billing_*.go    # 计费服务
│   │   │   ├── ops_*.go         # 运维监控
│   │   │   └── ...
│   │   ├── setup/               # 安装向导
│   │   ├── testutil/            # 测试工具
│   │   ├── util/                # 工具函数
│   │   └── web/                 # 前端资源嵌入
│   └── migrations/              # 数据库迁移 SQL
├── frontend/                    # Vue 3 前端 (需另查)
└── deploy/                      # 部署文件
    ├── docker-compose.yml
    ├── .env.example
    └── install.sh
```

---

## 四、核心模块分析

### 1. 入口程序 (`backend/cmd/server/main.go`)
- 使用 Wire 进行依赖注入
- 支持三种运行模式：首次安装向导、自动安装、正常服务
- 支持 HTTP/2 (h2c)

### 2. HTTP 层 (`backend/internal/server/`)
- **路由**: 基于 Gin 框架
- **中间件**: JWT 认证、API Key 认证、CORS、安全头、日志

### 3. Handler 层 (`backend/internal/handler/`)
处理各类 HTTP 请求：

| Handler | 职责 |
|---------|------|
| `AuthHandler` | 用户注册/登录/验证 |
| `UserHandler` | 用户管理 |
| `APIKeyHandler` | API Key 管理 |
| `GatewayHandler` | **核心网关**，代理 AI 请求 |
| `AdminHandlers` | 管理后台所有功能 |

### 4. Service 层 (`backend/internal/service/`)
核心业务逻辑：

| Service | 职责 |
|---------|------|
| `account_*` | 上游账号管理、OAuth、API Key |
| `gateway_*` | 请求路由、负载均衡、故障转移 |
| `auth_*` | 认证、Token 刷新 |
| `billing_*` | 计费、配额控制 |
| `ops_*` | 运维监控、指标收集 |

### 5. Repository 层 (`backend/internal/repository/`)
数据访问层，封装 Ent ORM 和 Redis 操作

### 6. 数据模型 (`backend/ent/schema/`)
数据库表结构：
- `user` - 用户
- `account` - 上游 AI 账号（支持 OAuth/API Key）
- `group` - 账号分组
- `apikey` - 用户 API Key
- `subscription` - 用户订阅
- `usagelog` - 用量记录
- `promocode` / `redeemcode` - 优惠码
- 等等...

---

## 五、数据流分析（核心网关）

```
用户请求 → API Key 验证 → GatewayHandler
    ↓
计费/配额检查
    ↓
账号选择 (智能调度: 负载均衡 + 粘性会话)
    ↓
请求转发到上游 AI (OpenAI/Anthropic/Gemini/Antigravity)
    ↓
响应回传 + 用量记录
```

### 关键服务
- **GatewayService**: 核心代理逻辑
- **AccountService**: 上游账号管理
- **AccountGroupService**: 账号分组调度
- **BillingService**: 计费与配额
- **RateLimitService**: 限流

---

## 六、依赖注入 (Wire)

项目使用 Google Wire 进行依赖注入：
- `backend/cmd/server/wire.go` - Wire 配置
- `backend/cmd/server/wire_gen.go` - 生成的依赖代码
- 各模块的 `wire.go` - 模块级依赖配置

---

## 七、关键特性

1. **多上游支持**: OpenAI, Anthropic (Claude), Gemini, Antigravity
2. **OAuth 集成**: 支持从平台直接 OAuth 授权获取上游账号
3. **智能调度**: 按配置权重、负载、延迟选择账号
4. **粘性会话**: 同一会话路由到同一账号
5. **并发控制**: 用户级和账号级并发限制
6. **精确计费**: Token 级别用量追踪
7. **运维监控**: 内置监控面板和告警

---

## 八、下一步阅读建议

1. **理解网关核心**: 查看 `handler/gateway_handler.go` 和 `service/gateway_service.go`
2. **理解账号调度**: 查看 `service/account_group.go`
3. **理解计费逻辑**: 查看 `service/billing_service.go`
4. **理解数据模型**: 查看 `ent/schema/` 目录

---

## 九、部署方式

- **脚本安装**: 一键安装脚本
- **Docker Compose**: 推荐的生产部署方式
- **源码编译**: 开发模式
