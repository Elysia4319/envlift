package webui

import (
	"io/fs"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// 本文件把 ③ 步的几条硬约束从「约定」变成可回归的断言。

// staticFiles 读出 embed 里的全部静态资源（路径 → 内容）。
func staticFiles(t *testing.T) map[string]string {
	t.Helper()
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		t.Fatalf("fs.Sub 失败: %v", err)
	}
	out := map[string]string{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 embed 失败: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("embed 里一个静态文件都没有")
	}
	return out
}

// C2：不允许任何外链。目标机可能断网，一个 CDN 引用就是整页白屏。
func TestStaticAssetsHaveNoExternalLinks(t *testing.T) {
	// 只允许回环地址：本地服务自己的地址出现在提示文案里是可以的。
	allowedHost := regexp.MustCompile(`(?i)^https?://(127\.0\.0\.1|localhost)(:\d+)?`)

	for name, body := range staticFiles(t) {
		for _, m := range regexp.MustCompile(`(?i)https?://[^\s"'<>()]+`).FindAllString(body, -1) {
			if !allowedHost.MatchString(m) {
				t.Errorf("%s 含外部链接（断网会白屏）: %s", name, m)
			}
		}
		// 协议相对写法同样会走网络。
		if regexp.MustCompile(`(?i)(src|href)\s*=\s*["']//`).MatchString(body) {
			t.Errorf("%s 含协议相对的外部引用", name)
		}
		// 外部字体常见写法 @import url(...)。
		if regexp.MustCompile(`(?i)@import\s+(url\()?["']?https?:`).MatchString(body) {
			t.Errorf("%s 通过 @import 引了外部样式", name)
		}
		// 子资源完整性校验属性意味着必然有外部资源。
		if strings.Contains(strings.ToLower(body), "integrity=") {
			t.Errorf("%s 含 integrity 属性", name)
		}
	}
}

// token 只能待在内存里：不落 localStorage/sessionStorage，也不留在地址栏。
func TestFrontendKeepsTokenInMemoryOnly(t *testing.T) {
	files := staticFiles(t)
	js, ok := files["app.js"]
	if !ok {
		t.Fatal("缺少 app.js")
	}
	for _, bad := range []string{"localStorage", "sessionStorage", "document.cookie"} {
		if strings.Contains(js, bad) {
			t.Errorf("app.js 不应使用 %s（token 只放内存）", bad)
		}
	}
	if !strings.Contains(js, "replaceState") {
		t.Error("app.js 应把 token 从地址栏抹掉（history.replaceState）")
	}
}

// C2：只用现有 /api/v1 路由，不为页面方便新增「前端专用」接口。
func TestFrontendUsesOnlyExistingRoutes(t *testing.T) {
	js := staticFiles(t)["app.js"]
	if js == "" {
		t.Fatal("缺少 app.js")
	}
	allowed := []string{
		"/api/v1/status", "/api/v1/scan", "/api/v1/scan/commit",
		"/api/v1/config/current", "/api/v1/config/load", "/api/v1/plan",
		"/api/v1/apply", "/api/v1/decisions", "/api/v1/task/", "/api/v1/reports",
		"/api/v1/rollback", "/api/quit",
	}
	// 抓取所有出现在字符串字面量里的 /api/... 前缀。
	refs := regexp.MustCompile(`['"](/api/[^'"]*)['"]`).FindAllStringSubmatch(js, -1)
	if len(refs) == 0 {
		t.Fatal("app.js 里没有找到任何 API 路径")
	}
	for _, r := range refs {
		p := r[1]
		okOne := false
		for _, a := range allowed {
			if p == a {
				okOne = true
				break
			}
			// 前缀只认「路径段边界」，否则 /api/v1/scanXXX 会被 /api/v1/scan 放行。
			if strings.HasPrefix(p, a) && (strings.HasSuffix(a, "/") || p[len(a)] == '/') {
				okOne = true
				break
			}
		}
		if !okOne {
			t.Errorf("app.js 引用了未登记的路由: %s", p)
		}
	}
}

// 前端不得自带业务逻辑：不拼 checkinstall 之类命令，也不做版本比较。
func TestFrontendDoesNotBuildCommands(t *testing.T) {
	js := staticFiles(t)["app.js"]
	for _, bad := range []string{"winget install", "--silent", "/VERYSILENT", "msiexec"} {
		if strings.Contains(js, bad) {
			t.Errorf("前端不应拼装安装命令（发现 %q）——命令只能由内核给出", bad)
		}
	}
}

// 安装目录输入必须存在，且只走已有决策接口，不引入外链。
func TestDecisionInstallDirControls(t *testing.T) {
	js := staticFiles(t)["app.js"]
	for _, want := range []string{"dec-installdir", "dec-apply-dir",
		"action: 'set_location'", "install_dir", "当前版本不支持与选包/选配方组合",
		"winget_force_ok", "将替换版本决策"} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 缺少安装目录控件或参数: %q", want)
		}
	}
}

func TestEmbedAssetsServedWithCorrectType(t *testing.T) {
	s := newTestServer(t)
	cases := []struct{ path, wantPrefix string }{
		{"/", "text/html; charset=utf-8"},
		{"/app.css", "text/css; charset=utf-8"},
		// Go 的 mime 表在 .js 上给的是 application/javascript（也见过 text/javascript），
		// 两者都是「能被浏览器当脚本执行」的类型，不锁死具体值。
		{"/app.js", "javascript"},
	}
	for _, c := range cases {
		w := do(s, c.path+"?token="+s.token, nil)
		if w.Code != http.StatusOK {
			t.Errorf("%s 应 200，实际 %d", c.path, w.Code)
			continue
		}
		got := w.Header().Get("Content-Type")
		if !strings.Contains(got, c.wantPrefix) {
			t.Errorf("%s Content-Type = %q，期望包含 %q", c.path, got, c.wantPrefix)
		}
	}
}

// /index.html 由 http.FileServer 301 到 "/"，这是标准库行为；两者都能取到首页。
func TestIndexHTMLRedirectsToRoot(t *testing.T) {
	s := newTestServer(t)
	w := do(s, "/index.html?token="+s.token, nil)
	if w.Code != http.StatusMovedPermanently && w.Code != http.StatusOK {
		t.Fatalf("/index.html 应 301 或 200，实际 %d", w.Code)
	}
	if w.Code == http.StatusMovedPermanently && !strings.HasPrefix(w.Header().Get("Location"), "./") {
		t.Errorf("重定向目标异常: %q", w.Header().Get("Location"))
	}
	root := do(s, "/?token="+s.token, nil)
	if !strings.Contains(root.Body.String(), "<title>envlift") {
		t.Error("根路径应返回首页")
	}
}

func TestIndexReferencesOnlyLocalAssets(t *testing.T) {
	html := staticFiles(t)["index.html"]
	for _, m := range regexp.MustCompile(`(?i)(src|href)\s*=\s*["']([^"']+)["']`).FindAllStringSubmatch(html, -1) {
		v := m[2]
		if strings.HasPrefix(v, "#") {
			continue
		}
		// data: 是内联内容（例如空 favicon 避免 404），不产生网络请求。
		if strings.HasPrefix(v, "data:") {
			continue
		}
		if strings.Contains(v, "://") || strings.HasPrefix(v, "//") {
			t.Errorf("index.html 引用了非本地资源: %s", v)
		}
	}
}

// 页面必须自带状态条所需的锚点，避免改前端时悄悄删掉提权徽章。
func TestIndexHasRequiredAnchors(t *testing.T) {
	html := staticFiles(t)["index.html"]
	for _, id := range []string{
		"id=\"badge-elev\"", "id=\"facts\"", "id=\"tab-scan\"", "id=\"tab-restore\"",
		"id=\"tab-path\"", "id=\"panel-scan\"", "id=\"panel-restore\"", "id=\"panel-path\"",
		"id=\"btn-quit\"", "id=\"errbar\"",
	} {
		if !strings.Contains(html, id) {
			t.Errorf("index.html 缺少 %s", id)
		}
	}
}

// D20：侧栏图标必须是内联 SVG——`<image>` 或外部 href 都会变成一次网络请求，
// 断网时图标消失（甚至拖慢首屏）。同时导航的方向语义必须写死一个初值，
// 否则竖排导航在 JS 跑起来之前对读屏器是不可理解的。
func TestNavIconsAreInlineSVG(t *testing.T) {
	html := staticFiles(t)["index.html"]
	if !strings.Contains(html, `<svg class="tab-icon"`) {
		t.Fatal("导航缺少内联 SVG 图标")
	}
	for _, bad := range []string{"<image", "xlink:href", "href=\"http"} {
		if strings.Contains(html, bad) {
			t.Errorf("内联 SVG 不得引用外部资源（发现 %q）", bad)
		}
	}
	if !strings.Contains(html, `aria-orientation="vertical"`) {
		t.Error("nav 应带 aria-orientation 初值（桌面竖排）")
	}
}

// D21：字体只允许系统栈，不得内嵌或外链字体文件。
func TestCSSUsesSystemFontsOnly(t *testing.T) {
	css := staticFiles(t)["app.css"]
	if css == "" {
		t.Fatal("缺少 app.css")
	}
	if strings.Contains(css, "@font-face") {
		t.Error("app.css 不得内嵌字体；未安装 JetBrains Mono 时应回退系统字体")
	}
	if !strings.Contains(css, `"JetBrains Mono"`) {
		t.Error("数据字体应优先使用 JetBrains Mono（未安装时自动回退）")
	}
	if !strings.Contains(css, "Segoe UI Variable Text") {
		t.Error("app.css 应使用系统字体栈")
	}
	// D22：主按钮是墨底纸字，两个 token 都必须显式存在。
	if !strings.Contains(css, "--ink") || !strings.Contains(css, "--paper") {
		t.Error("app.css 应显式定义墨色与纸色（主按钮不能吃默认继承色）")
	}
}

// D21：终端区域必须有 Mac 风格圆点与固定头部；日志正文单独滚动。
func TestTerminalUsesMacChrome(t *testing.T) {
	html := staticFiles(t)["index.html"]
	js := staticFiles(t)["app.js"]
	css := staticFiles(t)["app.css"]
	if !strings.Contains(html, `class="term-bar"`) {
		t.Error("扫描日志缺少 Mac 风格终端头部")
	}
	if !strings.Contains(js, `class="term-bar"`) {
		t.Error("执行日志缺少 Mac 风格终端头部")
	}
	if !strings.Contains(css, "JetBrains Mono") {
		t.Error("终端应优先使用 JetBrains Mono")
	}
	if !strings.Contains(css, ".term-bar i:nth-child(3)") {
		t.Error("终端头部应包含三个圆点")
	}
}
