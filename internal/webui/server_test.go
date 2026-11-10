package webui

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken 失败: %v", err)
	}
	s, err := New(tok)
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

func do(s *Server, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func TestBindsLoopbackOnly(t *testing.T) {
	s := newTestServer(t)
	addr, ok := s.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("地址类型异常: %T", s.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Errorf("只能绑回环地址，实际 %s", addr.IP)
	}
	if addr.Port == 0 {
		t.Error("端口应由系统分配为非 0")
	}
}

func TestURLContainsToken(t *testing.T) {
	s := newTestServer(t)
	if !strings.Contains(s.URL(), "127.0.0.1") || !strings.Contains(s.URL(), "token="+s.token) {
		t.Errorf("URL 格式不对: %s", s.URL())
	}
}

// D16：静态资源不鉴权（里面没有数据），但 /api/ 一律要 header。
func TestStaticIsOpenButAPIIsNot(t *testing.T) {
	s := newTestServer(t)
	for _, p := range []string{"/", "/app.css", "/app.js"} {
		if w := do(s, p, nil); w.Code != http.StatusOK {
			t.Errorf("%s 无 token 也应 200（静态资源不鉴权），实际 %d", p, w.Code)
		}
	}
	if w := do(s, "/api/health", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("/api/health 无 token 应 401，实际 %d", w.Code)
	}
}

func TestWrongTokenIs401(t *testing.T) {
	s := newTestServer(t)
	w := do(s, "/api/health", map[string]string{TokenHeader: "deadbeef"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("错误 token 应 401，实际 %d", w.Code)
	}
}

func TestCorrectTokenViaHeader(t *testing.T) {
	s := newTestServer(t)
	w := do(s, "/api/health", map[string]string{TokenHeader: s.token})
	if w.Code != http.StatusOK {
		t.Fatalf("正确 token 应 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("响应内容不对: %s", w.Body.String())
	}
}

// D16 的核心断言：任何响应都不许下发 Set-Cookie。
//
// cookie 一旦下发，token 就同时活在内存、地址栏历史和浏览器存储三处，
// 「只经请求头、只放内存」这句话就没法当断言用了。
func TestNoResponseSetsCookie(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name    string
		path    string
		headers map[string]string
	}{
		{"首页 + query token", "/?token=" + s.token, nil},
		{"首页无 token", "/", nil},
		{"静态资源", "/app.css?token=" + s.token, nil},
		{"API 带 header", "/api/health", map[string]string{TokenHeader: s.token}},
		{"API 无 token", "/api/health", nil},
	}
	for _, c := range cases {
		w := do(s, c.path, c.headers)
		if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("%s 下发了 Set-Cookie: %v", c.name, got)
		}
	}
}

// 只带 cookie、不带 header 的 API 请求必须 401——cookie 不再是任何形式的凭证。
func TestCookieIsNotACredential(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.AddCookie(&http.Cookie{Name: "envlift_token", Value: s.token})
	req.AddCookie(&http.Cookie{Name: "anything", Value: s.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("cookie 不应被当成凭证，实际 %d", w.Code)
	}
}

// /api/v1 的响应同样不许下发 Set-Cookie（含未授权时的 401）。
func TestAPIV1SetsNoCookie(t *testing.T) {
	s := newTestServer(t)
	s.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(TokenHeader) != s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"ok":true}`)
	})
	for _, h := range []map[string]string{nil, {TokenHeader: s.token}} {
		w := do(s, "/api/v1/status?token="+s.token, h)
		if got := w.Header().Values("Set-Cookie"); len(got) != 0 {
			t.Errorf("API 响应下发了 Set-Cookie: %v", got)
		}
	}
}

// API 不接受 cookie 与 query，只认请求头。
func TestAPIIgnoresCookieAndQuery(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/health?token="+s.token, nil)
	req.AddCookie(&http.Cookie{Name: "envlift_token", Value: s.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("API 不应接受 cookie/query，实际 %d", w.Code)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	s := newTestServer(t)
	for _, p := range []string{
		"/?token=" + s.token + "&x=1",
		"/../server.go?token=" + s.token,
		"/static/../../server.go?token=" + s.token,
	} {
		w := do(s, p, nil)
		body := w.Body.String()
		if strings.Contains(body, "package webui") {
			t.Errorf("%s 读到了源码，路径穿越没挡住", p)
		}
	}
}

func TestUnknownPathIs404(t *testing.T) {
	s := newTestServer(t)
	w := do(s, "/nope?token="+s.token, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的路径应 404，实际 %d", w.Code)
	}
}

func TestNoStoreHeader(t *testing.T) {
	s := newTestServer(t)
	w := do(s, "/?token="+s.token, nil)
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("静态资源应禁用缓存，实际 %q", got)
	}
}
