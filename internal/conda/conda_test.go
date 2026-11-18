package conda

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
)

type fakeRunner struct {
	calls   []Command
	respond func(Command) Result
}

func (f *fakeRunner) Run(_ context.Context, cmd Command, _ time.Duration) Result {
	f.calls = append(f.calls, cmd)
	if f.respond != nil {
		return f.respond(cmd)
	}
	return Result{ExitCode: 0}
}

type fakeFS struct {
	files map[string]bool
}

type fakeInfo struct {
	name string
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

func (f *fakeFS) MkdirAll(string, os.FileMode) error { return nil }
func (f *fakeFS) WriteFile(string, []byte, os.FileMode) error {
	return nil
}
func (f *fakeFS) RemoveAll(string) error { return nil }
func (f *fakeFS) Stat(path string) (os.FileInfo, error) {
	if f.files[normalizePath(path)] {
		return fakeInfo{name: filepath.Base(path)}, nil
	}
	return nil, errors.New("not exist")
}

func TestScanCollectsAllCondaInstallsAndFiltersBootstrap(t *testing.T) {
	root := `C:\Users\olduser\miniconda3`
	exe := filepath.Join(root, "Scripts", "conda.exe")
	named := filepath.Join(root, "envs", "科研环境")
	fs := &fakeFS{files: map[string]bool{
		normalizePath(exe): true,
		normalizePath(filepath.Join(root, "python.exe")):  true,
		normalizePath(filepath.Join(named, "python.exe")): true,
	}}
	runner := &fakeRunner{respond: func(cmd Command) Result {
		key := strings.ToLower(strings.Join(cmd.Args, " "))
		switch {
		case strings.Contains(key, "info --json"):
			return Result{Output: `{"conda_version":"24.1.2","root_prefix":"` + strings.ReplaceAll(root, `\`, `\\`) + `"}`}
		case strings.Contains(key, "env list --json"):
			return Result{Output: `{"envs":["` + strings.ReplaceAll(root, `\`, `\\`) + `","` +
				strings.ReplaceAll(named, `\`, `\\`) + `","` +
				strings.ReplaceAll(strings.ToLower(root), `\`, `\\`) + `"]}`}
		case strings.Contains(key, "list --json --prefix"):
			if strings.Contains(key, "科研") {
				return Result{Output: `[
					{"name":"python","version":"3.11.9","build":"h1","channel":"defaults"},
					{"name":"numpy","version":"1.26.4","build":"py311_0","channel":"https://user:pass@example.com/conda?token=abc"},
					{"name":"cloudpickle","version":"3.1.2","build":"pypi_0","channel":"pypi"},
					{"name":"pip","version":"24.0","build":"pyhd","channel":"defaults"}
				]`}
			}
			return Result{Output: `[{"name":"python","version":"3.12.1","build":"h1","channel":"defaults"}]`}
		case strings.Contains(strings.ToLower(cmd.Path), "envs\\科研") && strings.Contains(key, "pip list"):
			return Result{Output: `[
				{"name":"pip","version":"24.0"},
				{"name":"setuptools","version":"69.0"},
				{"name":"wheel","version":"0.42"},
				{"name":"numpy","version":"1.26.4"},
				{"name":"cloudpickle","version":"3.1.2"},
				{"name":"requests","version":"2.31.0"}
			]`}
		case strings.Contains(key, "pip list"):
			return Result{Output: `[{"name":"pip","version":"24.0"},{"name":"setuptools","version":"69.0"}]`}
		default:
			return Result{ExitCode: -1, Err: errors.New("unexpected command")}
		}
	}}
	s := &Scanner{
		Runner:   runner,
		FS:       fs,
		LookPath: func(string) (string, error) { return exe, nil },
		Getenv:   func(string) string { return "" },
		Timeout:  time.Second,
	}
	res, err := s.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan 失败: %v", err)
	}
	if !res.CondaFound || len(res.Envs) != 2 {
		t.Fatalf("应发现 2 个环境，实际 found=%v envs=%d", res.CondaFound, len(res.Envs))
	}
	var base, sci *config.CondaEnv
	for i := range res.Envs {
		switch res.Envs[i].Name {
		case "base":
			base = &res.Envs[i]
		case "科研环境":
			sci = &res.Envs[i]
		}
	}
	if base == nil || sci == nil {
		t.Fatalf("缺少 base 或命名环境: %+v", res.Envs)
	}
	if !base.Base || base.CondaEnabled() || base.PipEnabled() {
		t.Fatalf("base 应只读: %+v", base)
	}
	if sci.CondaEnabled() || sci.PipEnabled() {
		t.Fatalf("命名环境默认应不恢复: %+v", sci)
	}
	if sci.PythonVersion != "3.11.9" {
		t.Fatalf("PythonVersion=%q", sci.PythonVersion)
	}
	if len(sci.PipPackages) != 2 {
		t.Fatalf("pip 应保留 cloudpickle + requests: %+v", sci.PipPackages)
	}
	for _, p := range sci.PipPackages {
		switch NormalizePipName(p.Name) {
		case "requests", "cloudpickle":
		default:
			t.Fatalf("pip 清单包含不应出现的包: %+v", sci.PipPackages)
		}
	}
	ch := ""
	for _, p := range sci.Packages {
		if p.Name == "numpy" {
			ch = p.Channel
		}
		if p.Name == "cloudpickle" {
			t.Fatalf("channel=pypi 的包不应进入 conda 清单: %+v", p)
		}
	}
	if strings.Contains(ch, "pass") || strings.Contains(ch, "abc") {
		t.Fatalf("channel 凭据未脱敏: %q", ch)
	}
	if !strings.Contains(ch, "example.com") {
		t.Fatalf("channel 主机丢失: %q", ch)
	}
	req := string(RequirementsTXT(*sci))
	if strings.Contains(strings.ToLower(req), "numpy") {
		t.Fatalf("requirements.txt 不应重复包含 conda 包 numpy: %s", req)
	}
	if !strings.Contains(strings.ToLower(req), "requests") {
		t.Fatalf("requirements.txt 应包含 pip 包 requests: %s", req)
	}
}

func TestEnvironmentYAMLAndCompare(t *testing.T) {
	env := config.CondaEnv{
		Name: "py311", Prefix: `C:\My Apps\py311`, PythonVersion: "3.11.9",
		Packages: []config.CondaPackage{
			{Name: "numpy", Version: "1.26.4", Build: "py311_0", Channel: "conda-forge"},
		},
		PipPackages: []config.PipPackage{{Name: "Requests", Version: "2.31.0"}},
	}
	yes := true
	env.RestorePip = &yes
	data, err := EnvironmentYAML(env, nil)
	if err != nil {
		t.Fatalf("EnvironmentYAML: %v", err)
	}
	s := string(data)
	for _, want := range []string{"name: py311", "conda-forge::numpy=1.26.4=py311_0", "python=3.11.9", "pip"} {
		if !strings.Contains(s, want) {
			t.Errorf("environment.yml 缺少 %q:\n%s", want, s)
		}
	}
	req := string(RequirementsTXT(env))
	if !strings.Contains(req, "Requests==2.31.0") {
		t.Errorf("requirements.txt 缺少请求: %s", req)
	}
	if got := ComparePip(env.PipPackages, []config.PipPackage{{Name: "requests", Version: "2.31.0"}}); len(got) != 0 {
		t.Errorf("pip 名称规范化后应匹配: %v", got)
	}
	if got := CompareConda(env.Packages, []config.CondaPackage{{Name: "numpy", Version: "1.25.0", Build: "x"}}); len(got) == 0 {
		t.Error("版本不符应报告")
	}
}

func TestPipConfigRequiresExplicitTrustedHost(t *testing.T) {
	p := &config.MirrorProfile{
		PipIndexURL:    "http://mirror.local/simple",
		PipTrustedHost: []string{"mirror.local"},
	}
	cfg := string(PipConfig(p))
	if !strings.Contains(cfg, "trusted-host = mirror.local") {
		t.Fatalf("PipConfig 缺少 trusted-host: %s", cfg)
	}
	if strings.Contains(cfg, "token") || strings.Contains(cfg, "password") {
		t.Fatalf("PipConfig 不应含凭据: %s", cfg)
	}
}

func TestFilterPipBootstrapOption(t *testing.T) {
	in := []config.PipPackage{
		{Name: "pip", Version: "24"}, {Name: "setuptools", Version: "69"},
		{Name: "wheel", Version: "0.42"}, {Name: "requests", Version: "2.31"},
	}
	got := filterPipBootstrap(in, false)
	if len(got) != 1 || got[0].Name != "requests" {
		t.Fatalf("默认应过滤基础包: %+v", got)
	}
	kept := filterPipBootstrap(in, true)
	if len(kept) != 4 {
		t.Fatalf("保留选项应保留基础包: %+v", kept)
	}
}

func TestPipConfigMultipleExtraIndexURLs(t *testing.T) {
	p := &config.MirrorProfile{
		PipIndexURL:   "https://pypi.example/simple",
		PipExtraIndex: []string{"https://a.example/simple", "https://b.example/simple"},
	}
	cfg := string(PipConfig(p))
	if strings.Count(cfg, "extra-index-url = ") != 2 {
		t.Fatalf("应写两行 extra-index-url: %s", cfg)
	}
	for _, want := range []string{"a.example", "b.example"} {
		if !strings.Contains(cfg, want) {
			t.Fatalf("PipConfig 缺少 %s: %s", want, cfg)
		}
	}
}
