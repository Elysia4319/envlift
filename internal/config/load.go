package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// fileHeader 写在配置文件顶部，便于人直接读。
const fileHeader = `# envlift 配置文件（schema v1）
# 由 envlift scan 生成，可手工编辑。
# 脱敏占位符：${USER} = 用户名，${HOST} = 主机名。
`

// migrations[n] 把 schema n-1 的配置迁移到 schema n。
// 目前只有 v1，尚无迁移步骤；保留骨架供后续版本使用（D9）。
var migrations = map[int]func(*Config) error{}

// Load 从 path 读取并校验配置。
//
// 高版本配置直接拒绝（D9）；低版本配置先备份原文件再链式迁移。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}
	cfg, err := LoadBytes(data)
	if err != nil {
		return nil, err
	}
	if cfg.Schema < CurrentSchema {
		if err := migrate(path, cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// LoadBytes 从字节解析配置，不做文件层面的备份与迁移。
func LoadBytes(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false) // 未知字段落入 Extra，读端忽略但保留（D9）
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// migrate 把低版本配置升级到 CurrentSchema，迁移前把原文件备份为 <path>.bak。
func migrate(path string, cfg *Config) error {
	if err := copyFile(path, path+".bak"); err != nil {
		return fmt.Errorf("迁移前备份 %s 失败: %w", path, err)
	}
	for v := cfg.Schema + 1; v <= CurrentSchema; v++ {
		fn, ok := migrations[v]
		if !ok {
			return fmt.Errorf("缺少 schema %d→%d 的迁移步骤", v-1, v)
		}
		if err := fn(cfg); err != nil {
			return fmt.Errorf("迁移 schema %d→%d 失败: %w", v-1, v, err)
		}
		cfg.Schema = v
	}
	return nil
}

// Save 把配置写入 path。已存在同名文件时先备份为 <path>.bak，再原子替换。
//
// 不覆盖用户已有配置的约束在这里由「先备份」保证（D11 的例外条款同源）。
func Save(path string, cfg *Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}

	if _, err := os.Stat(path); err == nil {
		if err := copyFile(path, path+".bak"); err != nil {
			return fmt.Errorf("备份已有配置 %s 失败: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查已有配置 %s 失败: %w", path, err)
	}

	var buf bytes.Buffer
	buf.WriteString(fileHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Excluded 返回 x-excluded 区里的软件条目。
//
// 该区来自扫描时的「排除导出」（③-A2）：这些条目不在 software: 里，但仍留在
// 配置中。读盘后 Extra 里是 []interface{}（yaml 的默认表示），内存中可能是
// []Software，两种形态都要认——否则「排除项不能被下决策」这条校验会形同虚设。
func (c *Config) Excluded() []Software {
	if c == nil || c.Extra == nil {
		return nil
	}
	raw, ok := c.Extra["x-excluded"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case []Software:
		return v
	case []interface{}:
		out := make([]Software, 0, len(v))
		for _, e := range v {
			b, err := yaml.Marshal(e)
			if err != nil {
				continue
			}
			var sw Software
			if err := yaml.Unmarshal(b, &sw); err != nil {
				continue
			}
			out = append(out, sw)
		}
		return out
	}
	return nil
}
