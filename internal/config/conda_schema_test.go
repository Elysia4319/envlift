package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchemaV2CondaRoundTrip(t *testing.T) {
	src := `
app: envlift
schema: 2
generated_at: '2026-11-18T10:00:00+08:00'
source_machine: {os: {version: W, build: '1'}, arch: amd64, hostname: '${HOST}', username: '${USER}'}
path: {user: [], machine: []}
runtimes: []
software: []
conda_envs:
  - name: py311
    prefix: 'C:\Users\${USER}\miniconda3\envs\py311'
    manager: 'C:\Users\${USER}\miniconda3'
    conda_exe: 'C:\Users\${USER}\miniconda3\Scripts\conda.exe'
    conda_version: '24.1.2'
    python_version: '3.11.9'
    restore_conda: true
    restore_pip: false
    mirror: corp
    packages:
      - {name: numpy, version: '1.26.4', build: py311_0, channel: conda-forge}
    pip_packages:
      - {name: requests, version: '2.31.0'}
mirrors:
  corp:
    conda_channels: [conda-forge]
    pip_index_url: 'https://mirror.example/simple'
`
	cfg, err := LoadBytes([]byte(src))
	if err != nil {
		t.Fatalf("LoadBytes 失败: %v", err)
	}
	if cfg.Schema != 2 || len(cfg.CondaEnvs) != 1 || len(cfg.Mirrors) != 1 {
		t.Fatalf("v2 conda 字段未解析: %+v", cfg)
	}
	if cfg.CondaEnvs[0].CondaEnabled() != true || cfg.CondaEnvs[0].PipEnabled() != false {
		t.Fatalf("restore 开关解析错误: %+v", cfg.CondaEnvs[0])
	}
}

func TestCondaGoldenLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("testdata", "conda-v2.golden.yaml"))
	if err != nil {
		t.Fatalf("golden 加载失败: %v", err)
	}
	if len(cfg.CondaEnvs) != 1 || cfg.CondaEnvs[0].Name != "py311" {
		t.Fatalf("golden conda 字段不对: %+v", cfg.CondaEnvs)
	}
	if cfg.CondaEnvs[0].Packages[0].Channel != "conda-forge" {
		t.Fatalf("golden 包字段不对: %+v", cfg.CondaEnvs[0].Packages)
	}
}

func TestV1ConfigMigratesToV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "envlift.yaml")
	src := `
app: envlift
schema: 1
generated_at: '2026-10-27T10:05:00+08:00'
source_machine: {os: {version: W, build: '1'}, arch: amd64, hostname: h, username: u}
path: {user: [], machine: []}
runtimes: []
software: []
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load v1 失败: %v", err)
	}
	if cfg.Schema != CurrentSchema {
		t.Fatalf("迁移后 schema=%d，期望 %d", cfg.Schema, CurrentSchema)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("迁移前应备份 .bak: %v", err)
	}
}

func TestMirrorRejectsCredentialsAndPlainHTTP(t *testing.T) {
	cases := []struct {
		name string
		p    MirrorProfile
	}{
		{"userinfo", MirrorProfile{PipIndexURL: "https://user:pass@example.com/simple"}},
		{"token", MirrorProfile{PipIndexURL: "https://example.com/simple?token=abc"}},
		{"http-without-trust", MirrorProfile{PipIndexURL: "http://example.com/simple"}},
		{"git", MirrorProfile{PipIndexURL: "git+https://example.com/repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				App: AppName, Schema: CurrentSchema,
				SourceMachine: SourceMachine{Arch: "amd64", Hostname: "${HOST}", Username: "${USER}"},
				Path:          PathSection{User: []PathEntry{}, Machine: []PathEntry{}},
				Runtimes:      []Runtime{}, Software: []Software{},
				Mirrors: map[string]MirrorProfile{"bad": tc.p},
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("应拒绝: %+v", tc.p)
			}
		})
	}
}

func TestCondaEnvRequiresPrefixAndKnownMirror(t *testing.T) {
	cfg := &Config{
		App: AppName, Schema: CurrentSchema,
		SourceMachine: SourceMachine{Arch: "amd64", Hostname: "${HOST}", Username: "${USER}"},
		Path:          PathSection{User: []PathEntry{}, Machine: []PathEntry{}},
		Runtimes:      []Runtime{}, Software: []Software{},
		CondaEnvs: []CondaEnv{{Name: "py311"}},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("缺 prefix 应报错: %v", err)
	}
	cfg.CondaEnvs[0].Prefix = `C:\env`
	cfg.CondaEnvs[0].Mirror = "missing"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mirror") {
		t.Fatalf("未知 mirror 应报错: %v", err)
	}
}

func TestCondaRestoreDefaultsFalse(t *testing.T) {
	env := CondaEnv{Name: "py311", Prefix: `C:\env`}
	if env.CondaEnabled() || env.PipEnabled() {
		t.Fatal("未显式设置的 conda/pip 开关必须默认 false")
	}
}

func TestSchemaV3Rejected(t *testing.T) {
	src := `
app: envlift
schema: 3
generated_at: '2026-11-18T10:00:00+08:00'
source_machine: {os: {version: W, build: '1'}, arch: amd64, hostname: h, username: u}
path: {user: [], machine: []}
runtimes: []
software: []
`
	_, err := LoadBytes([]byte(src))
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("schema=3 应拒绝: %v", err)
	}
}
