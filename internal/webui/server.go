// Package webui 提供 GUI 的本地 HTTP 服务。
//
// 设计要点（D14/C-1）：
//   - 零第三方依赖，前端用 embed 编进 exe，仍是单文件；
//   - 只绑 127.0.0.1，端口由系统分配（:0），天然避开端口占用；
//   - 一次性随机 token，所有请求都要校验，缺失或错误一律 401；
//   - 进程退出即释放端口，不留后台常驻。
package webui

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"time"
)

//go:embed static
var staticFS embed.FS

// TokenHeader 是前端携带 token 的请求头名。
//
// 与 api 包里的常量取值一致（HTTP 头名大小写不敏感，但只保留一种拼写，
// 免得两边各自「看起来一样」而实际上传了两个不同的名字）。
const TokenHeader = "X-Envlift-Token"

// Server 是本地 GUI 服务。
type Server struct {
	// API 由 api 包提供，已自带 /api/v1 前缀与鉴权。
	API   http.Handler
	token string
	ln    net.Listener
	srv   *http.Server
	quit  chan struct{}
}

// NewToken 生成一次性随机 token。
func NewToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成 token 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// New 在 127.0.0.1 上绑定一个空闲端口。
func New(token string) (*Server, error) { return NewOn(token, 0) }

// NewOn 在指定端口绑定；port=0 表示由系统分配。
//
// 指定端口被占用时回落到系统分配（D-2 的「重试」在固定端口上没有意义，
// 换个空闲端口比失败退出更符合使用预期）。
func NewOn(token string, port int) (*Server, error) {
	if token == "" {
		return nil, fmt.Errorf("token 不能为空")
	}
	// 只绑回环地址：GUI 不能被局域网访问。
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil && port != 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return nil, fmt.Errorf("绑定 127.0.0.1 失败: %w", err)
	}
	s := &Server{token: token, ln: ln, quit: make(chan struct{})}
	// Handler 在 Serve 时才构建：调用方需要在 New 之后挂载 s.API，
	// 这里提前构建会把 nil 的 API 固化进 mux，导致 /api/v1 全部 404。
	s.srv = &http.Server{ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

// URL 返回带 token 的访问地址。
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?token=%s", s.ln.Addr().String(), s.token)
}

// Quit 在收到 /api/quit 后关闭，供 main 等待退出。
func (s *Server) Quit() <-chan struct{} { return s.quit }

// Addr 返回监听地址，便于测试断言只绑了回环。
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Serve 开始服务，阻塞直到 Shutdown。
func (s *Server) Serve() error {
	if s.srv.Handler == nil {
		s.srv.Handler = s.Handler()
	}
	err := s.srv.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Shutdown 优雅关闭并释放端口。
func (s *Server) Shutdown() error {
	return s.srv.Close()
}

// Handler 返回静态资源与本地 API 的路由，便于 httptest 直接测。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(TokenHeader) != s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"ok":true,"app":"envlift"}`)
	})
	// 显式「退出」按钮用这个端点；进程退出即释放端口，不留后台常驻（C-1）。
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(TokenHeader) != s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"ok":true,"message":"shutting down"}`)
		select {
		case <-s.quit:
		default:
			close(s.quit)
		}
	})
	// API 由 api 包自己鉴权（只认请求头）。
	if s.API != nil {
		mux.Handle("/api/v1/", s.API)
	}
	// 静态资源不鉴权（D16）：里面没有任何数据，只有 HTML/CSS/JS。
	// 它们能被任何人读到也没关系，而鉴权链路越短越好。
	mux.Handle("/", http.HandlerFunc(s.serveStatic))
	return mux
}

// serveStatic 从 embed.FS 提供前端资源。
//
// 用 http.FileServer + fs.Sub：路径清理与越界拦截由标准库负责，
// 不自己拼路径（自己拼才是路径穿越的常见来源）。
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "静态资源不可用", http.StatusInternalServerError)
		return
	}
	// 前端是编进 exe 的固定内容，缓存没意义，反而会导致改了看不到。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if _, err := fs.Stat(sub, p); err != nil {
		http.NotFound(w, r)
		return
	}
	http.FileServer(http.FS(sub)).ServeHTTP(w, r)
}
