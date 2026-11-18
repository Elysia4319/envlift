package targetid

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Elysia4319/envlift/internal/config"
)

func sw(name, version, hive, arch, loc, publisher string) config.Software {
	return config.Software{
		Name:            name,
		Version:         config.StringPtr(version),
		Hive:            config.StringPtr(hive),
		ArchView:        config.StringPtr(arch),
		InstallLocation: config.StringPtr(loc),
		Publisher:       config.StringPtr(publisher),
	}
}

// 同内容必然同 id——这是「重跑一次 plan，决策还认得上」的前提。
func TestSameContentSameID(t *testing.T) {
	a := sw("Git", "2.47.1", "HKLM", "64", `C:\Program Files\Git`, "The Git Development Community")
	b := sw("Git", "2.47.1", "HKLM", "64", `C:\Program Files\Git`, "The Git Development Community")
	if Software(a) != Software(b) {
		t.Fatalf("同内容 id 不同: %s vs %s", Software(a), Software(b))
	}
	if got := len(Software(a)); got != idLen {
		t.Errorf("id 长度 = %d，期望 %d", got, idLen)
	}
}

// 大小写、首尾空白、尾部反斜杠都属于「显然等价」，不该改变 id。
func TestNormalizationDoesNotChangeID(t *testing.T) {
	base := sw("Git", "2.47.1", "HKLM", "64", `C:\Program Files\Git`, "Git")
	for _, variant := range []config.Software{
		sw("  git ", "2.47.1", "hklm", "64", `c:\program files\git\`, "git"),
		sw("GIT", "2.47.1 ", "HKLM", "64", `C:\Program Files\Git`, " GIT "),
	} {
		if Software(base) != Software(variant) {
			t.Errorf("归一化后应同 id: %q vs %q", base.Name, variant.Name)
		}
	}
}

// 版本差一位就必须是另一个 id，否则「装哪个版本」的决策会张冠李戴。
func TestVersionChangeChangesID(t *testing.T) {
	a := sw("Git", "2.47.1", "HKLM", "64", `C:\Program Files\Git`, "Git")
	b := sw("Git", "2.47.2", "HKLM", "64", `C:\Program Files\Git`, "Git")
	if Software(a) == Software(b) {
		t.Fatal("版本不同却算出同一个 id")
	}
}

// 根键与架构视图进 scope：同名的两条装在不同位置就是两条东西。
func TestScopeChangeChangesID(t *testing.T) {
	base := sw("App", "1.0", "HKLM", "64", `C:\App`, "V")
	if Software(base) == Software(sw("App", "1.0", "HKCU", "64", `C:\App`, "V")) {
		t.Error("hive 不同却同 id")
	}
	if Software(base) == Software(sw("App", "1.0", "HKLM", "32", `C:\App`, "V")) {
		t.Error("arch_view 不同却同 id")
	}
	if Software(base) == Software(sw("App", "1.0", "HKLM", "64", `D:\App`, "V")) {
		t.Error("安装位置不同却同 id")
	}
}

// 顺序无关：id 只由条目自身内容决定，不依赖它在清单里的位置。
func TestIDIsOrderIndependent(t *testing.T) {
	list := []config.Software{
		sw("A", "1", "HKLM", "64", `C:\A`, "p"),
		sw("B", "2", "HKLM", "64", `C:\B`, "p"),
		sw("C", "3", "HKLM", "64", `C:\C`, "p"),
	}
	first := []string{Software(list[0]), Software(list[1]), Software(list[2])}
	// 换个顺序（以及完全反过来）逐个取 id，同一个条目必须还是同一个 id
	rev := []config.Software{list[2], list[1], list[0]}
	if Software(rev[0]) != first[2] || Software(rev[1]) != first[1] || Software(rev[2]) != first[0] {
		t.Error("换顺序后同一个条目的 id 变了")
	}
}

func TestPathID(t *testing.T) {
	u := Path("user", `C:\Tools\bin`)
	if u != Path("user", `c:\tools\bin\`) {
		t.Error("同一条 PATH 因大小写/尾斜杠算出不同 id")
	}
	if u == Path("machine", `C:\Tools\bin`) {
		t.Error("用户级与系统级同值应得到不同 id（合并后值可能相同）")
	}
	if u == Path("user", `C:\Tools\bin2`) {
		t.Error("不同路径不应同 id")
	}
	if len(u) != idLen {
		t.Errorf("id 长度 = %d", len(u))
	}
}

// 软件与 PATH 的 id 空间不能互相撞上（kind 必须参与哈希）。
func TestKindIsPartOfID(t *testing.T) {
	s := Key{Kind: "software", Name: "x", Value: `C:\x`}.ID()
	p := Key{Kind: "path", Name: "x", Value: `C:\x`}.ID()
	if s == p {
		t.Error("kind 不同却同 id")
	}
	if strings.TrimSpace(s) == "" {
		t.Error("id 不应为空")
	}
}

// A1：字段缺失（nil）与空串必须算出同一个 id。
//
// 这是「同一份配置在另一台机器上重新扫描后决策还认得上」的前提：不同来源对
// 缺失的表示并不一致（winget 源无 install_location、App Paths 无版本）。
func TestMissingEqualsEmpty(t *testing.T) {
	nilFields := config.Software{
		Name:     "App",
		Version:  config.StringPtr("1.0"),
		Hive:     config.StringPtr("HKLM"),
		ArchView: config.StringPtr("64"),
		// Publisher / InstallLocation 明确为 nil
	}
	emptyFields := config.Software{
		Name:            "App",
		Version:         config.StringPtr("1.0"),
		Hive:            config.StringPtr("HKLM"),
		ArchView:        config.StringPtr("64"),
		Publisher:       config.StringPtr(""),
		InstallLocation: config.StringPtr(""),
	}
	if Software(nilFields) != Software(emptyFields) {
		t.Fatalf("nil 与空串算出不同 id: %s vs %s", Software(nilFields), Software(emptyFields))
	}

	// 只缺 publisher 也要等价
	a := sw("App", "1.0", "HKLM", "64", `C:\\App`, "")
	b := sw("App", "1.0", "HKLM", "64", `C:\\App`, "")
	b.Publisher = nil
	if Software(a) != Software(b) {
		t.Error("publisher 为 nil 与空串不应改变 id")
	}

	// 只缺 install_location 也要等价
	c := sw("App", "1.0", "HKLM", "64", "", "V")
	d := sw("App", "1.0", "HKLM", "64", "", "V")
	d.InstallLocation = nil
	if Software(c) != Software(d) {
		t.Error("install_location 为 nil 与空串不应改变 id")
	}
}

// A4：字段里带分隔符不能造成「串位」碰撞。
//
// {name:"a|b", version:""} 与 {name:"a", version:"b"} 朴素拼接后都是 "a|b"。
func TestDelimiterDoesNotCollide(t *testing.T) {
	a := Key{Kind: KindSoftware, Name: "a|b"}
	b := Key{Kind: KindSoftware, Name: "a", Version: "b"}
	if a.ID() == b.ID() {
		t.Fatalf("分隔符碰撞：name=%q 与 name=%q version=%q 算出了同一个 id", "a|b", "a", "b")
	}

	// 另一组：值从后一个字段挪到前一个字段（空值错位）
	c := Key{Kind: KindSoftware, Name: "x", Version: "y"}
	d := Key{Kind: KindSoftware, Name: "x|y"}
	if c.ID() == d.ID() {
		t.Fatal("空值错位碰撞：name=x version=y 与 name=x|y 同 id")
	}

	// 全空字段应与零值 Key 同 id（长度前缀不能因空串退化）
	e := Key{Kind: KindSoftware}
	f := Key{Kind: KindSoftware, Publisher: "", Name: "", Version: "", Scope: "", Value: ""}
	if e.ID() != f.ID() {
		t.Error("全空字段应与零值 Key 同 id")
	}
}

// A2：runtime 的 scope 必须区分「谁在管」。
//
// fnm 管理的 node 21 与系统安装的 node 21，kind/name/version 完全相同，
// 但一个是 fnm install、一个是 winget install，决策绝不能串台。
func TestRuntimeManagerChangesID(t *testing.T) {
	base := config.Runtime{
		Name:    "node",
		Version: config.StringPtr("21.0.0"),
		Manager: config.StringPtr("fnm"),
	}
	sys := config.Runtime{
		Name:    "node",
		Version: config.StringPtr("21.0.0"),
	}
	if Runtime(base) == Runtime(sys) {
		t.Fatal("fnm 托管的 node 21 与系统 node 21 算出了同一个 id")
	}
	if got := RuntimeScope(base); got != "managed:fnm" {
		t.Errorf("managed runtime 的 scope = %q，期望 %q", got, "managed:fnm")
	}
	if got := RuntimeScope(sys); got != "system" {
		t.Errorf("非托管 runtime 的 scope = %q，期望 %q", got, "system")
	}

	// 不同管理器托管的同名同版本运行时也必须分开
	conda := config.Runtime{Name: "node", Version: config.StringPtr("21.0.0"), Manager: config.StringPtr("conda")}
	if Runtime(base) == Runtime(conda) {
		t.Error("fnm 与 conda 托管的 node 21 不应同 id")
	}

	// 空串 manager 与 nil manager 等价（A1 的同类规则）
	emptyMgr := config.Runtime{Name: "node", Version: config.StringPtr("21.0.0"), Manager: config.StringPtr("")}
	if Runtime(emptyMgr) != Runtime(sys) {
		t.Error("manager 为空串与 nil 不应改变 id")
	}
}

// 软件与运行时同名不同 kind → 不同 id（决策不能跨类命中）。
func TestRuntimeAndSoftwareDiffer(t *testing.T) {
	swID := Key{Kind: KindSoftware, Name: "node", Version: "21.0.0"}.ID()
	rtID := Runtime(config.Runtime{Name: "node", Version: config.StringPtr("21.0.0")})
	if swID == rtID {
		t.Fatal("software 与 runtime 同名同版本算出了同一个 id")
	}
}

// A3：id 不进 schema、不落盘——配置序列化里不能出现 target_id。
func TestIDIsNotPersisted(t *testing.T) {
	cfg := config.Config{
		App:      config.AppName,
		Schema:   config.CurrentSchema,
		Software: []config.Software{sw("App", "1.0", "HKLM", "64", `C:\\App`, "V")},
		Runtimes: []config.Runtime{{Name: "node", Version: config.StringPtr("21.0.0")}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "target_id") {
		t.Fatalf("配置 JSON 里出现了 target_id，说明 id 被落盘了: %s", data)
	}
	// 同一份内容重算必须稳定（重跑 plan 后决策仍能命中）
	if Software(cfg.Software[0]) != Software(cfg.Software[0]) {
		t.Error("同一条目两次计算 id 不一致")
	}
}

func TestCondaEnvIDStableAcrossPrefix(t *testing.T) {
	a := config.CondaEnv{Name: "py311", Prefix: `C:\Users\alice\miniconda3\envs\py311`, PythonVersion: "3.11.9"}
	b := config.CondaEnv{Name: "py311", Prefix: `D:\conda\envs\py311`, PythonVersion: "3.11.9"}
	if CondaEnv(a) != CondaEnv(b) {
		t.Fatalf("conda 环境 id 不应依赖 prefix: %s != %s", CondaEnv(a), CondaEnv(b))
	}
	b.PythonVersion = "3.12.1"
	if CondaEnv(a) == CondaEnv(b) {
		t.Fatal("不同 Python 版本应得到不同 id")
	}
}
