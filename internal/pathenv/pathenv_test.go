package pathenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 测试全部在 t.TempDir() 里造真实文件，注入假 PATH，
// 不读本机注册表、不依赖本机真实 PATH（A6）。

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("stub"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// lookup 是测试用的 Lookup：命中即返回原路径（不做大小写修正）。
func lookup(p string) (string, bool) {
	if isFile(p) {
		return p, true
	}
	return "", false
}

func TestResolvePrefersPathExtOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "python.bat"))
	writeFile(t, filepath.Join(dir, "python.exe"))

	dirs := []Dir{{Path: dir, Source: SourceMachine, Index: 0}}
	got, ok := Resolve("python", dirs, SplitPathExt(DefaultPathExt), lookup)
	if !ok {
		t.Fatal("应解析成功")
	}
	// Windows 文件系统大小写不敏感：路径里的扩展名大小写来自 PATHEXT 而非磁盘，
	// 所以这里按大小写不敏感比较。
	if !strings.EqualFold(filepath.Ext(got.Path), ".exe") {
		t.Errorf("应按 PATHEXT 顺序先试 .EXE，实际: %s", got.Path)
	}
}

func TestResolveFallsBackToBat(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "nvm.bat"))

	dirs := []Dir{{Path: dir, Source: SourceMachine, Index: 0}}
	got, ok := Resolve("nvm", dirs, SplitPathExt(DefaultPathExt), lookup)
	if !ok {
		t.Fatal("应解析成功")
	}
	if !strings.EqualFold(filepath.Ext(got.Path), ".bat") {
		t.Errorf("应落到 .bat，实际: %s", got.Path)
	}
}

func TestResolveSegmentOrderMachineBeforeUser(t *testing.T) {
	machineDir := t.TempDir()
	userDir := t.TempDir()
	writeFile(t, filepath.Join(machineDir, "python.exe"))
	writeFile(t, filepath.Join(userDir, "python.exe"))

	dirs := BuildDirs([]string{machineDir}, []string{userDir}, nil)
	got, ok := Resolve("python", dirs, SplitPathExt(DefaultPathExt), lookup)
	if !ok {
		t.Fatal("应解析成功")
	}
	if got.Source != SourceMachine {
		t.Errorf("系统段应优先于用户段，实际来源: %s", got.Source)
	}
	if filepath.Dir(got.Path) != machineDir {
		t.Errorf("应命中系统段目录，实际: %s", got.Path)
	}
}

func TestResolveIndexWithinSegment(t *testing.T) {
	empty := t.TempDir()
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "git.exe"))

	dirs := []Dir{
		{Path: empty, Source: SourceMachine, Index: 0},
		{Path: real, Source: SourceMachine, Index: 1},
	}
	got, ok := Resolve("git", dirs, SplitPathExt(DefaultPathExt), lookup)
	if !ok {
		t.Fatal("应解析成功")
	}
	if got.Index != 1 {
		t.Errorf("来源索引应为 1，实际: %d", got.Index)
	}
}

func TestResolveLiteralExtension(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "tool.exe"))
	writeFile(t, filepath.Join(dir, "tool.cmd"))

	dirs := []Dir{{Path: dir, Source: SourceUser, Index: 0}}
	got, ok := Resolve("tool.cmd", dirs, SplitPathExt(DefaultPathExt), lookup)
	if !ok {
		t.Fatal("带扩展名时应按字面解析成功")
	}
	if !strings.EqualFold(filepath.Ext(got.Path), ".cmd") {
		t.Errorf("带扩展名时不应再试其他扩展名，实际: %s", got.Path)
	}
}

func TestResolveMissing(t *testing.T) {
	dir := t.TempDir()
	dirs := []Dir{{Path: dir, Source: SourceUser, Index: 0}}
	if _, ok := Resolve("nothing-here", dirs, SplitPathExt(DefaultPathExt), lookup); ok {
		t.Error("不存在的命令不应解析成功")
	}
}

func TestResolveSessionFallback(t *testing.T) {
	machineDir := t.TempDir()
	sessionDir := t.TempDir()
	writeFile(t, filepath.Join(sessionDir, "only-in-session.exe"))

	dirs := BuildDirs([]string{machineDir}, nil, []string{sessionDir})
	got, ok := Resolve("only-in-session", dirs, SplitPathExt(DefaultPathExt), lookup)
	if !ok {
		t.Fatal("会话兜底应生效")
	}
	if got.Source != SourceSession {
		t.Errorf("来源应为 session，实际: %s", got.Source)
	}
}

func TestBuildDirsDeduplicates(t *testing.T) {
	dirs := BuildDirs(
		[]string{`C:\A`},
		[]string{`C:\B`},
		[]string{`C:\A`, `C:\C`}, // C:\A 已在系统段，不应重复
	)
	want := []struct {
		path   string
		source string
	}{
		{`C:\A`, SourceMachine},
		{`C:\B`, SourceUser},
		{`C:\C`, SourceSession},
	}
	if len(dirs) != len(want) {
		t.Fatalf("目录数 = %d，期望 %d: %#v", len(dirs), len(want), dirs)
	}
	for i, w := range want {
		if dirs[i].Path != w.path || dirs[i].Source != w.source {
			t.Errorf("第 %d 个 = {%s,%s}，期望 {%s,%s}", i, dirs[i].Path, dirs[i].Source, w.path, w.source)
		}
	}
}

func TestSplitPathExtNormalizes(t *testing.T) {
	got := SplitPathExt("com;.exe;BAT")
	want := []string{".COM", ".EXE", ".BAT"}
	if len(got) != len(want) {
		t.Fatalf("扩展名数 = %d，期望 %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q，期望 %q", i, got[i], want[i])
		}
	}

	// 空值回落到内置默认。
	def := SplitPathExt("")
	if len(def) != 4 || def[1] != ".EXE" {
		t.Errorf("空 PATHEXT 应回落默认，实际: %#v", def)
	}
}
