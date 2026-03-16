/*
 * Sub2API 主程序入口
 *
 * 本文件是 Sub2API 应用的入口点，负责：
 * 1. 解析命令行参数（--setup, --version）
 * 2. 检测是否需要首次安装向导
 * 3. 初始化日志系统
 * 4. 加载配置并启动主服务器
 * 5. 处理优雅关闭
 *
 * 运行模式：
 * - 普通模式：启动完整的后端服务
 * - 安装向导模式：首次运行时启动 Web 安装界面
 * - CLI 模式：通过 --setup 参数启动命令行安装
 */
package main

/*
 * Wire 依赖注入代码生成指令
 *
 * go:generate 是 Go 的代码生成机制
 * 这行注释告诉 Go 在执行 go generate 时调用 wire 工具
 * Wire 是 Google 开发的依赖注入框架，会根据 wire.go 中的定义
 * 自动生成依赖注入代码到 wire_gen.go 文件中
 *
 * 使用方法：cd backend && go generate ./cmd/server
 */
//go:generate go run github.com/google/wire/cmd/wire

import (
	"context"
	_ "embed"   // embed 包用于将静态文件嵌入到二进制中
	"errors"    // 错误处理
	"flag"      // 命令行参数解析
	"log"       // 日志输出
	"net/http"  // HTTP 服务器
	"os"        // 操作系统交互
	"os/signal" // 信号处理
	"strings"   // 字符串处理
	"syscall"   // 系统调用
	"time"      // 时间处理

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"              // Ent 运行时初始化
	"github.com/Wei-Shaw/sub2api/internal/config"            // 配置管理
	"github.com/Wei-Shaw/sub2api/internal/handler"           // HTTP 处理器
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"        // 日志系统
	"github.com/Wei-Shaw/sub2api/internal/server/middleware" // 中间件
	"github.com/Wei-Shaw/sub2api/internal/setup"             // 安装向导
	"github.com/Wei-Shaw/sub2api/internal/web"               // 前端资源

	"github.com/gin-gonic/gin"   // Web 框架
	"golang.org/x/net/http2"     // HTTP/2 协议
	"golang.org/x/net/http2/h2c" // HTTP/2 over TCP (不需 TLS)
)

//go:embed VERSION
/*
 * VERSION 文件嵌入
 *
 * //go:embed 是 Go 1.16+ 的特性，用于在编译时将文件内容嵌入到变量中
 * 这里嵌入 VERSION 文件的内容，用于在运行时获取应用版本号
 * VERSION 文件在构建时由 goreleaser 或手动创建，包含类似 "1.0.0" 的版本字符串
 */
var embeddedVersion string

/*
 * 构建时变量（可通过 ldflags 在构建时注入）
 *
 * 这些变量用于存储版本信息，通常在 CI/CD 流水线中通过 -X 标志注入：
 *   go build -ldflags "-X main.Version=1.0.0 -X main.Commit=$(git rev-parse HEAD) -X main.Date=$(date -u +%Y-%m-%d)"
 *
 * - Version: 版本号，如 "1.0.0"
 * - Commit: Git 提交哈希
 * - Date: 构建日期
 * - BuildType: 构建类型，"source" 表示手动构建，"release" 表示 CI 构建
 */
var (
	Version   = ""        // 版本号
	Commit    = "unknown" // Git 提交哈希
	Date      = "unknown" // 构建日期
	BuildType = "source"  // "source" 手动构建, "release" CI 构建
)

/*
 * init 函数：版本号初始化
 *
 * init() 是 Go 的初始化函数，在 main() 之前自动执行
 * 此处逻辑：
 * 1. 如果 Version 已通过 ldflags 注入，直接使用
 * 2. 否则从 embedded VERSION 文件读取
 * 3. 如果都为空，默认为 "0.0.0-dev"
 */
func init() {
	// 如果 Version 已通过 ldflags 注入（例如 -X main.Version=...），则不要覆盖。
	if strings.TrimSpace(Version) != "" {
		return
	}

	// 默认从 embedded VERSION 文件读取版本号（编译期打包进二进制）。
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

/*
 * main 函数：应用入口
 *
 * 程序启动流程：
 * 1. 初始化日志系统（bootstrap 模式，用于早期日志）
 * 2. 解析命令行参数
 * 3. 如果是 --version，显示版本信息后退出
 * 4. 如果是 --setup，进入 CLI 安装模式
 * 5. 检查是否需要首次安装向导
 * 6. 如果需要且未配置自动安装，启动 Web 安装向导
 * 7. 否则启动主服务器
 */
func main() {
	// 初始化 bootstrap 日志（用于早期日志输出，此时配置可能还未加载）
	logger.InitBootstrap()
	defer logger.Sync() // 确保日志刷新

	// 解析命令行 flags
	// --setup: 进入 CLI 安装模式
	// --version: 显示版本信息
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	// 显示版本信息
	if *showVersion {
		log.Printf("Sub2API %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}

	// CLI 安装模式：非交互式安装
	if *setupMode {
		if err := setup.RunCLI(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	// 检查是否需要首次安装
	// Sub2API 首次启动时需要配置数据库、Redis 等，这是通过检测配置文件是否存在来实现的
	if setup.NeedsSetup() {
		// 检查是否启用了自动安装（用于 Docker 部署场景）
		// 自动安装会从环境变量读取配置
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				log.Fatalf("Auto setup failed: %v", err)
			}
			// 自动安装完成后继续启动主服务器
		} else {
			// 首次运行，启动 Web 安装向导
			log.Println("First run detected, starting setup wizard...")
			runSetupServer()
			return
		}
	}

	// 正常启动主服务器
	runMainServer()
}

/*
 * runSetupServer 启动首次安装向导服务器
 *
 * 当用户首次运行 Sub2API 时，会进入此模式
 * 这是一个精简的 HTTP 服务器，只提供安装功能：
 * - 数据库配置（PostgreSQL 连接信息）
 * - Redis 配置
 * - 管理员账户创建
 * - 初始化数据库表结构
 *
 * 安装完成后，程序会生成配置文件并退出
 * 下次启动将进入正常服务模式
 */
func runSetupServer() {
	// 创建 Gin 引擎
	// Gin 是高性能的 Go Web 框架
	r := gin.New()

	// 添加中间件
	r.Use(middleware.Recovery())                                                                             // 恐慌恢复，防止崩溃
	r.Use(middleware.CORS(config.CORSConfig{}))                                                              // 跨域资源共享配置
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil)) // 安全头

	// 注册安装路由
	// setup 包提供了完整的安装向导 HTTP 处理逻辑
	setup.RegisterRoutes(r)

	// 如果编译时嵌入了前端资源，也一并提供
	// 这允许用户在安装向导中看到 Web 界面
	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// 获取服务器地址
	// 可以通过配置文件或环境变量 (SERVER_HOST, SERVER_PORT) 自定义
	addr := config.GetServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure Sub2API")

	// 创建 HTTP 服务器
	// 使用 h2c.NewHandler 实现 HTTP/2 支持（不需 TLS）
	// h2c 是 "HTTP/2 over TCP" 的缩写，允许在非加密连接上使用 HTTP/2
	server := &http.Server{
		Addr:              addr,                               // 监听地址
		Handler:           h2c.NewHandler(r, &http2.Server{}), // HTTP/2 处理器
		ReadHeaderTimeout: 30 * time.Second,                   // 读取请求头超时
		IdleTimeout:       120 * time.Second,                  // 空闲连接超时
	}

	// 启动服务器并监听
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start setup server: %v", err)
	}
}

/*
 * runMainServer 启动主服务器
 *
 * 这是 Sub2API 的正常运行模式，启动完整的 Web 服务：
 * 1. 加载配置文件
 * 2. 初始化日志系统
 * 3. 初始化依赖注入（Wire 生成）
 * 4. 启动 HTTP 服务器
 * 5. 等待中断信号并优雅关闭
 */
func runMainServer() {
	// 第一步：加载引导配置
	// 此时只加载最基础的配置，用于后续初始化日志
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 第二步：根据配置初始化日志系统
	// 支持多种日志格式和输出目标
	if err := logger.Init(logger.OptionsFromConfig(cfg.Log)); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// 检查运行模式
	// Simple 模式：跳过计费检查，适合个人使用
	if cfg.RunMode == config.RunModeSimple {
		log.Println("⚠️  WARNING: Running in SIMPLE mode - billing and quota checks are DISABLED")
	}

	// 构建版本信息，用于在 API 或管理界面显示
	buildInfo := handler.BuildInfo{
		Version:   Version,
		BuildType: BuildType,
	}

	// 第三步：初始化应用（依赖注入）
	// initializeApplication 是 Wire 生成的函数
	// 它会根据 wire.go 中的定义，创建并组装所有依赖组件
	app, err := initializeApplication(buildInfo)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer app.Cleanup() // 确保退出时清理资源

	// 第四步：启动 HTTP 服务器（在后台运行）
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on %s", app.Server.Addr)

	// 第五步：等待中断信号，实现优雅关闭
	// 监听 SIGINT (Ctrl+C) 和 SIGTERM (kill 信号)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit // 阻塞直到收到信号

	log.Println("Shutting down server...")

	// 创建带超时的关闭上下文
	// 给服务器最多 5 秒时间处理现有请求
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 优雅关闭：等待现有请求处理完成
	if err := app.Server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
