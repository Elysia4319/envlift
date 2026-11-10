package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envlift.yaml")
	orig := sampleConfig()

	if err := Save(path, orig); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if len(got.Extra) == 0 {
		got.Extra = nil
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("往返不一致\n orig: %#v\n got:  %#v", orig, got)
	}
}

func TestSaveWritesHeaderAndBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envlift.yaml")
	cfg := sampleConfig()

	if err := Save(path, cfg); err != nil {
		t.Fatalf("首次 Save 失败: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Error("首次保存不应产生 .bak")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !strings.HasPrefix(string(data), "# envlift 配置文件") {
		t.Error("配置文件应以说明性头部注释开头")
	}

	cfg.Software[0].Version = StringPtr("2.48.0")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("二次 Save 失败: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("覆盖保存前应生成 .bak: %v", err)
	}
}

func TestUnknownFieldsPreserved(t *testing.T) {
	src := `
app: envlift
schema: 1
generated_at: '2026-10-27T10:05:00+08:00'
x-custom: hello
source_machine: {os: {version: W, build: '1'}, arch: amd64, hostname: '${HOST}', username: '${USER}'}
path: {user: [], machine: []}
runtimes: []
software: []
`
	cfg, err := LoadBytes([]byte(src))
	if err != nil {
		t.Fatalf("LoadBytes 失败: %v", err)
	}
	if got := cfg.Extra["x-custom"]; got != "hello" {
		t.Errorf("未知字段未保留: %#v", cfg.Extra)
	}

	path := filepath.Join(t.TempDir(), "envlift.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "x-custom: hello") {
		t.Error("回写时应保留 x- 自定义字段")
	}
}

func TestSchemaTooNewRejected(t *testing.T) {
	src := `
app: envlift
schema: 2
generated_at: '2026-10-27T10:05:00+08:00'
source_machine: {os: {version: W, build: '1'}, arch: amd64, hostname: h, username: u}
path: {user: [], machine: []}
runtimes: []
software: []
`
	_, err := LoadBytes([]byte(src))
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("schema=2 应返回 ErrSchemaTooNew，实际: %v", err)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		mod  func(*Config)
	}{
		{"app 名不符", func(c *Config) { c.App = "other" }},
		{"schema 非正", func(c *Config) { c.Schema = 0 }},
		{"配方缺 match", func(c *Config) { c.Installers[0].Match = "" }},
		{"配方 source 非法", func(c *Config) { c.Installers[0].Source = "guessed" }},
		{"配方重名", func(c *Config) {
			dup := c.Installers[0]
			dup.Name = strings.ToUpper(dup.Name) // 大小写不同也算重名
			c.Installers = append(c.Installers, dup)
		}},
	}
	for _, c := range cases {
		cfg := sampleConfig()
		c.mod(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: 应校验失败", c.name)
		}
	}
}

func TestGeneratedYAMLIsHumanReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "envlift.yaml")
	if err := Save(path, sampleConfig()); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	s := string(data)
	t.Log("\n" + s)

	if strings.Contains(s, "\t") {
		t.Error("配置不应出现制表符")
	}
	if !strings.Contains(s, "\napp: envlift\n") {
		t.Error("顶层键不应缩进")
	}
	if !strings.Contains(s, "\n  os:\n") {
		t.Error("source_machine.os 应为 2 空格缩进的嵌套对象")
	}
	if !strings.Contains(s, "version: null") {
		t.Error("可空字段应显式输出 null，便于人读")
	}
}

// TestExampleConfigIsValid 让 docs/envlift.example.yaml 同时充当 golden file：
// 示例文件一旦与 schema 脱节，这个测试就会红。
func TestExampleConfigIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "envlift.example.yaml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("示例配置无法加载: %v", err)
	}
	if cfg.Schema != CurrentSchema {
		t.Errorf("示例 schema = %d，期望 %d", cfg.Schema, CurrentSchema)
	}
	if len(cfg.Path.User) == 0 || len(cfg.Path.Machine) == 0 {
		t.Error("示例应覆盖 PATH 两个分段")
	}
	if len(cfg.Runtimes) == 0 || len(cfg.Software) == 0 || len(cfg.Installers) == 0 {
		t.Error("示例应覆盖 runtimes / software / installers")
	}
	if cfg.DriveMap["C"].Purpose != "" {
		t.Error("示例中 purpose 应为空，表示由人手工填写")
	}
	// 示例里应同时出现 REG_SZ 与 REG_EXPAND_SZ 两种条目。
	var sawExpand bool
	for _, e := range cfg.Path.Machine {
		if e.Expand {
			sawExpand = true
		}
	}
	if !sawExpand {
		t.Error("示例应包含至少一条 expand: true 的条目")
	}
}
