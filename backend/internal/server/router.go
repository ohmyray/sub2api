/*
 * Sub2API 路由设置模块
 *
 * 本文件负责配置 Gin 引擎的中间件和注册所有 HTTP 路由
 * 是整个 Web 服务的入口点，决定了请求如何被处理和分发
 */
package server

/*
 * 导入说明：
 *
 * - context: Go 标准库，用于创建带超时的上下文
 * - log: 日志输出
 * - sync/atomic: 原子操作，用于线程安全的缓存更新
 * - time: 时间处理
 *
 * - config: 配置管理
 * - handler: HTTP 请求处理器
 * - middleware: HTTP 中间件（认证、日志、安全等）
 * - routes: 路由注册函数
 * - service: 业务逻辑服务
 * - web: 前端资源嵌入
 *
 * - gin: Web 框架
 * - redis: Redis 客户端
 */
import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/web"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

/*
 * 常量说明：
 *
 * frameSrcRefreshTimeout: iframe 来源列表的刷新超时时间
 * 用于动态获取允许嵌入的外部页面 origin 列表
 * 这是为了支持管理后台的外部系统集成功能（如支付页面）
 */
const frameSrcRefreshTimeout = 5 * time.Second

/*
 * SetupRouter 配置路由器中间件和路由
 *
 * 这是路由设置的核心函数，由 Wire 依赖注入调用
 * 负责：
 * 1. 配置全局中间件（日志、CORS、安全头）
 * 2. 设置前端资源嵌入（如果编译时包含前端）
 * 3. 注册所有业务路由
 *
 * 参数说明：
 * - r: Gin 引擎实例
 * - handlers: 所有 HTTP 处理器（通过 Wire 注入）
 * - jwtAuth: JWT 认证中间件
 * - adminAuth: 管理员认证中间件
 * - apiKeyAuth: API Key 认证中间件
 * - apiKeyService: API Key 服务
 * - subscriptionService: 订阅服务
 * - opsService: 运维服务
 * - settingService: 设置服务
 * - cfg: 应用配置
 * - redisClient: Redis 客户端
 *
 * 返回值：
 * - 配置好的 Gin 引擎实例
 */
func SetupRouter(
	r *gin.Engine,
	handlers *handler.Handlers,
	jwtAuth middleware2.JWTAuthMiddleware,
	adminAuth middleware2.AdminAuthMiddleware,
	apiKeyAuth middleware2.APIKeyAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	cfg *config.Config,
	redisClient *redis.Client,
) *gin.Engine {
	/*
	 * 动态 CSP frame-src 刷新机制
	 *
	 * CSP (Content Security Policy) 是一种安全策略，用于防止 XSS 攻击
	 * frame-src 指令控制页面中 <iframe> 可以嵌入的来源
	 *
	 * 这里使用原子指针实现线程安全的缓存：
	 * - cachedFrameOrigins 存储允许嵌入的 origin 列表
	 * - refreshFrameOrigins 函数定期刷新这个列表
	 * - 从 settingService 获取配置，支持运行时更新
	 */
	var cachedFrameOrigins atomic.Pointer[[]string]
	emptyOrigins := []string{}
	cachedFrameOrigins.Store(&emptyOrigins)

	/*
	 * refreshFrameOrigins 刷新 iframe 来源列表
	 *
	 * 从设置服务获取允许嵌入的外部系统 origin
	 * 用于管理后台通过 iframe 嵌入第三方页面（如支付系统）
	 * 失败时保留旧缓存，避免误清空导致嵌入功能失效
	 */
	refreshFrameOrigins := func() {
		ctx, cancel := context.WithTimeout(context.Background(), frameSrcRefreshTimeout)
		defer cancel()
		origins, err := settingService.GetFrameSrcOrigins(ctx)
		if err != nil {
			// 获取失败时保留已有缓存，避免 frame-src 被意外清空
			return
		}
		cachedFrameOrigins.Store(&origins)
	}
	refreshFrameOrigins() // 启动时初始化

	/*
	 * 应用全局中间件
	 *
	 * 中间件是处理请求的"过滤器"，按顺序执行：
	 * 1. RequestLogger: 请求日志，记录每个请求的详细信息
	 * 2. Logger: Gin 内置日志中间件
	 * 3. CORS: 跨域资源共享配置，允许前端跨域访问 API
	 * 4. SecurityHeaders: 安全响应头（X-Frame-Options, CSP 等）
	 */
	r.Use(middleware2.RequestLogger())
	r.Use(middleware2.Logger())
	r.Use(middleware2.CORS(cfg.CORS))
	// SecurityHeaders 的 frame-src 来自动态缓存
	r.Use(middleware2.SecurityHeaders(cfg.Security.CSP, func() []string {
		if p := cachedFrameOrigins.Load(); p != nil {
			return *p
		}
		return nil
	}))

	/*
	 * 前端资源嵌入处理
	 *
	 * Sub2API 支持将前端 Vue 应用编译嵌入到后端二进制中
	 * 这样部署时只需分发一个二进制文件，无需单独部署前端
	 *
	 * 支持两种模式：
	 * 1. 高级模式 (FrontendServer): 支持运行时配置注入到 HTML 中
	 * 2. 传统模式: 直接返回静态文件
	 *
	 * 设置服务更新时，会同时：
	 * - 失效前端 HTML 缓存
	 * - 刷新 iframe origin 列表
	 */
	if web.HasEmbeddedFrontend() {
		frontendServer, err := web.NewFrontendServer(settingService)
		if err != nil {
			log.Printf("Warning: Failed to create frontend server with settings injection: %v, using legacy mode", err)
			r.Use(web.ServeEmbeddedFrontend())
			settingService.SetOnUpdateCallback(refreshFrameOrigins)
		} else {
			// 注册组合回调：失效 HTML 缓存 + 刷新 frame origins
			settingService.SetOnUpdateCallback(func() {
				frontendServer.InvalidateCache()
				refreshFrameOrigins()
			})
			r.Use(frontendServer.Middleware())
		}
	} else {
		settingService.SetOnUpdateCallback(refreshFrameOrigins)
	}

	// 注册所有业务路由
	registerRoutes(r, handlers, jwtAuth, adminAuth, apiKeyAuth, apiKeyService, subscriptionService, opsService, settingService, cfg, redisClient)

	return r
}

/*
 * registerRoutes 注册所有 HTTP 路由
 *
 * 路由结构：
 * - /: 前端页面（嵌入模式）
 * - /api/v1: API 路由组
 *   - /api/v1/auth/*: 认证相关（登录、注册、OAuth）
 *   - /api/v1/user/*: 用户管理
 *   - /api/v1/admin/*: 管理后台（需管理员权限）
 *   - /api/v1/sora-client/*: Sora 客户端接口
 * - /v1/*: 网关路由（API Key 认证）
 * - /health: 健康检查
 *
 * 参数说明：
 * 各参数与 SetupRouter 相同，负责将处理器挂载到对应路径
 */
func registerRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	jwtAuth middleware2.JWTAuthMiddleware,
	adminAuth middleware2.AdminAuthMiddleware,
	apiKeyAuth middleware2.APIKeyAuthMiddleware,
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	opsService *service.OpsService,
	settingService *service.SettingService,
	cfg *config.Config,
	redisClient *redis.Client,
) {
	/*
	 * 通用路由
	 *
	 * 包括健康检查、状态检查等不需要认证的接口
	 * /health: 返回 {"status": "ok"}，用于容器健康检查
	 */
	routes.RegisterCommonRoutes(r)

	/*
	 * API v1 路由组
	 *
	 * 所有 API 接口都在 /api/v1 前缀下
	 * 使用路由组可以统一添加中间件（如认证）
	 */
	v1 := r.Group("/api/v1")

	/*
	 * 注册各模块路由
	 *
	 * - AuthRoutes: 认证路由（登录、注册、OAuth）
	 * - UserRoutes: 用户路由（个人资料、设置）
	 * - SoraClientRoutes: Sora 客户端路由
	 * - AdminRoutes: 管理后台路由（需 adminAuth）
	 * - GatewayRoutes: API 网关路由（需 apiKeyAuth）
	 */
	routes.RegisterAuthRoutes(v1, h, jwtAuth, redisClient, settingService)
	routes.RegisterUserRoutes(v1, h, jwtAuth, settingService)
	routes.RegisterSoraClientRoutes(v1, h, jwtAuth, settingService)
	routes.RegisterAdminRoutes(v1, h, adminAuth)
	routes.RegisterGatewayRoutes(r, h, apiKeyAuth, apiKeyService, subscriptionService, opsService, settingService, cfg)
}
