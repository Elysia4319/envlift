package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 逐字段断言 YAML 名与 JSON 名一致，而不是抽查几个键：
// 抽查过的那几个永远是对的，漏掉的字段才会在界面上变成 undefined。
func TestEveryYAMLFieldHasSameJSONName(t *testing.T) {
	types := []any{
		Config{}, SourceMachine{}, OSInfo{}, DriveInfo{}, PathSection{},
		PathEntry{}, Runtime{}, Software{}, Installer{}, Verify{},
	}
	for _, typ := range types {
		rt := reflect.TypeOf(typ)
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			yamlTag := f.Tag.Get("yaml")
			jsonTag := f.Tag.Get("json")
			yamlName := strings.Split(yamlTag, ",")[0]
			jsonName := strings.Split(jsonTag, ",")[0]

			if yamlName == "-" || yamlTag == "" {
				continue
			}
			if yamlName == "" {
				// 内联字段（Extra）不参与 JSON。
				if jsonName != "-" {
					t.Errorf("%s.%s 是内联字段，JSON 标签应为 \"-\"，实际 %q", rt.Name(), f.Name, jsonTag)
				}
				continue
			}
			if jsonName != yamlName {
				t.Errorf("%s.%s 的 YAML 名 %q 与 JSON 名 %q 不一致（前端会读不到）",
					rt.Name(), f.Name, yamlName, jsonName)
			}
		}
	}
}

// GUI 直接把 config.Config 编成 JSON 交给前端，所以 JSON 键必须与 yaml 键同名。
//
// 这条曾经是个真 bug：结构体只有 yaml 标签时，JSON 键会退化成 Go 字段名
// （App / Schema / SourceMachine），前端读 cfg.schema 永远拿到 undefined，
// 页面上一片「?」——不报错，只是全空，属于最难查的那种。
func TestConfigJSONUsesSameKeysAsYAML(t *testing.T) {
	cfg := &Config{
		App:    AppName,
		Schema: CurrentSchema,
		SourceMachine: SourceMachine{
			OS:       OSInfo{Version: "Windows 11", Build: "26300", ProductName: "x"},
			Arch:     "amd64",
			Hostname: "HOST",
			Username: "USER",
		},
		Path: PathSection{
			User:    []PathEntry{{Value: `C:\a`, Expand: true}},
			Machine: []PathEntry{{Value: `C:\b`}},
		},
		Runtimes: []Runtime{{Name: "node", Command: "node"}},
		Software: []Software{{Name: "Git", Sources: []string{"registry"}}},
		Installers: []Installer{{
			Name: "git", Match: "*.exe", SilentArgs: "/S", Source: InstallerUser,
		}},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	s := string(b)
	for _, key := range []string{
		`"app"`, `"schema"`, `"generated_at"`, `"source_machine"`, `"path"`,
		`"runtimes"`, `"software"`, `"installers"`, `"username"`, `"hostname"`,
		`"uninstall_string"`, `"winget_id"`, `"silent_args"`, `"expect_reboot"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON 缺少小写下划线键 %s: %s", key, s)
		}
	}
	for _, bad := range []string{`"App"`, `"Schema"`, `"SourceMachine"`, `"Software"`} {
		if strings.Contains(s, bad) {
			t.Errorf("JSON 里出现了 Go 字段名键 %s: %s", bad, s)
		}
	}
}

// JSON 往返必须无损：GUI 读到的配置与 yaml 读到的是同一份。
func TestConfigJSONRoundTrip(t *testing.T) {
	orig := &Config{
		App: AppName, Schema: CurrentSchema,
		Path:     PathSection{User: []PathEntry{{Value: `C:\x`, Expand: true}}},
		Software: []Software{{Name: "A", Sources: []string{"winget"}}},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal 失败: %v", err)
	}
	var back Config
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("Unmarshal 失败: %v", err)
	}
	if back.App != orig.App || back.Schema != orig.Schema ||
		len(back.Path.User) != 1 || back.Path.User[0].Value != `C:\x` || !back.Path.User[0].Expand ||
		len(back.Software) != 1 || back.Software[0].Name != "A" {
		t.Errorf("JSON 往返丢字段: %+v", back)
	}
}
