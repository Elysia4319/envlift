// Package config 定义 envlift 配置文件的 schema、读写、脱敏与版本迁移。
//
// schema 定义与 docs/PRD.md §3 一一对应；任何字段改动都必须先改 PRD。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	// AppName 是配置文件 app 字段的固定值。
	AppName = "envlift"
	// CurrentSchema 是本程序支持的配置 schema 主版本。
	CurrentSchema = 2
)

// ErrSchemaTooNew 表示配置的 schema 版本高于本程序支持的版本。
//
// 按 D9：高版本一律拒绝加载，不做降级读取——降级读取会静默丢失语义。
var ErrSchemaTooNew = errors.New("配置 schema 版本高于本程序支持")

// Config 是 envlift.yaml 的根结构。
//
// 每个字段都同时带 yaml 与 json 标签，且取值一致：GUI 的 /api/v1 直接把这份
// 结构编成 JSON 给前端，若只有 yaml 标签，JSON 键会退化成 Go 字段名（App/Schema），
// 前端只能靠大小写猜——那是隐式的第二套命名约定，迟早出错。
type Config struct {
	App           string               `yaml:"app" json:"app"`
	Schema        int                  `yaml:"schema" json:"schema"`
	GeneratedAt   string               `yaml:"generated_at" json:"generated_at"`
	SourceMachine SourceMachine        `yaml:"source_machine" json:"source_machine"`
	DriveMap      map[string]DriveInfo `yaml:"drive_map,omitempty" json:"drive_map,omitempty"`
	Path          PathSection          `yaml:"path" json:"path"`
	Runtimes      []Runtime            `yaml:"runtimes" json:"runtimes"`
	Software      []Software           `yaml:"software" json:"software"`
	Installers    []Installer          `yaml:"installers,omitempty" json:"installers,omitempty"`
	// CondaEnvs 是只读扫描得到的 conda 命名环境。base 默认只读，不参与自动恢复。
	CondaEnvs []CondaEnv `yaml:"conda_envs,omitempty" json:"conda_envs,omitempty"`
	// Mirrors 是命名镜像 profile；scan 不写入默认镜像，避免把某个公共源硬编码成事实。
	Mirrors map[string]MirrorProfile `yaml:"mirrors,omitempty" json:"mirrors,omitempty"`
	Extra   map[string]interface{}   `yaml:",inline" json:"-"`
}

// SourceMachine 记录来源机器的基本信息。hostname 与 username 默认已脱敏。
type SourceMachine struct {
	OS       OSInfo `yaml:"os" json:"os"`
	Arch     string `yaml:"arch" json:"arch"`
	Hostname string `yaml:"hostname" json:"hostname"`
	Username string `yaml:"username" json:"username"`
}

// OSInfo 是系统版本信息。
//
// Version 是按 build number 判定的主版本（可能为 unknown）；ProductName 是注册表
// 原值，**不可靠**（本机实测 build 26300 却报 "Windows 10 Home China"），仅作参考；
// Build 保留原值，判定出错时能事后看出来（B2/B3）。
type OSInfo struct {
	Version        string `yaml:"version" json:"version"`
	Build          string `yaml:"build" json:"build"`
	ProductName    string `yaml:"product_name" json:"product_name"`
	DisplayVersion string `yaml:"display_version,omitempty" json:"display_version,omitempty"`
}

// DriveInfo 是 drive_map 的值。仅作还原判断的元信息，不承担脱敏职责（D5-R1）。
type DriveInfo struct {
	Label   string `yaml:"label" json:"label"`
	Purpose string `yaml:"purpose" json:"purpose"`
}

// PathSection 保存 PATH 的两类持久值，不包含当前会话 PATH（D2）。
type PathSection struct {
	User    []PathEntry `yaml:"user" json:"user"`
	Machine []PathEntry `yaml:"machine" json:"machine"`
}

// PathEntry 是 PATH 中的一条目。
//
// Value 保留注册表里的原始字面值，**不展开** %SystemRoot% 这类变量——展开会把
// 机器相关值固化进配置，还原时也失去判断依据（B1）。Expand 标记来源类型是否为
// REG_EXPAND_SZ，仅作信息记录，还原时原样写回即可由系统自行展开。
type PathEntry struct {
	Value  string `yaml:"value" json:"value"`
	Expand bool   `yaml:"expand,omitempty" json:"expand,omitempty"`
}

// Runtime 是一个被探测到的语言运行时。
type Runtime struct {
	Name       string  `yaml:"name" json:"name"`
	Command    string  `yaml:"command" json:"command"`
	Version    *string `yaml:"version" json:"version"`
	Manager    *string `yaml:"manager" json:"manager"`
	Executable *string `yaml:"executable" json:"executable"`
	// ResolvedFrom / ResolvedIndex 记录可执行文件来自哪个 PATH 段、段内第几条，
	// 回答「为什么解析到这个路径」（A5）。literal 表示命令名本身带路径。
	ResolvedFrom  *string `yaml:"resolved_from,omitempty" json:"resolved_from,omitempty"`
	ResolvedIndex *int    `yaml:"resolved_index,omitempty" json:"resolved_index,omitempty"`
}

// Software 是一条已装软件记录。只记事实，不记安装命令与安装参数（D2）。
//
// 全部字段除 name 外均可选：读不到就留空，不报错（M6/B）。
type Software struct {
	// Name 是注册表 DisplayName 原值，未做任何规范化。
	Name      string  `yaml:"name" json:"name"`
	Publisher *string `yaml:"publisher" json:"publisher"`
	Version   *string `yaml:"version" json:"version"`
	// UninstallString 是注册表原值，仅作事实记录，**不用它反推静默参数**（D2）。
	UninstallString *string `yaml:"uninstall_string" json:"uninstall_string"`
	InstallLocation *string `yaml:"install_location" json:"install_location"`
	WingetID        *string `yaml:"winget_id" json:"winget_id"`
	// Sources 记录该条目来自哪些源：uninstall / winget / app_paths。
	Sources []string `yaml:"sources" json:"sources"`
	// ArchView 记录注册表视图：64 或 32。
	ArchView *string `yaml:"arch_view" json:"arch_view"`
	// Hive 记录来源根键：HKLM / HKCU。用户级安装不需管理员，去重时优先（A2）。
	Hive *string `yaml:"hive,omitempty" json:"hive,omitempty"`
	// Pinned 由用户显式标记（x- 前缀，D9）。同一配方多个条目时，
	// 被 pin 的条目优先当代表（A2）。
	Pinned        *bool   `yaml:"x-pinned,omitempty" json:"x-pinned,omitempty"`
	SilentCapable bool    `yaml:"silent_capable" json:"silent_capable"`
	ManualHint    *string `yaml:"manual_hint" json:"manual_hint"`
}

// Installer 是一条安装配方（D6）。
//
// 包目录不放元数据，靠 Match 的文件名 glob 匹配；配方与软件条目的对应关系
// 按 M2 的四级锚点判定：winget_ids → aliases → match_display → name。
type Installer struct {
	Name string `yaml:"name" json:"name"`
	// WingetIDs 是**最强锚点**（M1）：winget id 规范化、稳定、不带版本与厂商噪声，
	// 而 DisplayName 是最脏的字段，所以优先用它。
	WingetIDs []string `yaml:"winget_ids,omitempty" json:"winget_ids,omitempty"`
	// Aliases 是规范化后精确匹配的别名（M2 第二级）。
	Aliases []string `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	// MatchDisplay 是对 DisplayName 的正则（Go RE2，建议带 (?i) 并锚定 ^...$）。
	MatchDisplay string `yaml:"match_display,omitempty" json:"match_display,omitempty"`
	// MultiVersion 表示该产品允许多版本共存（A1）。
	//
	// false（默认）：同一配方的多个条目去重，只保留一个代表，其余标 covered。
	// true：每个版本都是独立条目，各自执行——JDK / Python / Node / .NET SDK
	// 这类「项目各自钉版本」的运行时必须置 true，否则只装一个版本是错的。
	MultiVersion bool   `yaml:"multi_version,omitempty" json:"multi_version,omitempty"`
	Match        string `yaml:"match" json:"match"`
	SilentArgs   string `yaml:"silent_args" json:"silent_args"`
	// SilentArgsSource 记录静默参数的依据（B4）：官方文档链接或本机实测结论。
	// **不许凭印象填**——编造的静默参数比没有配方危险得多：dry-run 会显示一条
	// 看起来正确、执行却静默失败的命令。留空表示该配方只用于识别，不参与本地包安装。
	SilentArgsSource string   `yaml:"silent_args_source,omitempty" json:"silent_args_source,omitempty"`
	NeedsAdmin       bool     `yaml:"needs_admin" json:"needs_admin"`
	Order            int      `yaml:"order,omitempty" json:"order,omitempty"`
	DependsOn        []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	// InstallDirArg / WingetLocationArg 是「用户指定安装目录」的显式模板。
	// 模板中用 {install_dir} 表示用户选择的目录；空表示该配方不支持指定目录。
	// 前者追加到本地安装包命令，后者追加到 winget 命令；两者可同时声明。
	InstallDirArg     string `yaml:"install_dir_arg,omitempty" json:"install_dir_arg,omitempty"`
	WingetLocationArg string `yaml:"winget_location_arg,omitempty" json:"winget_location_arg,omitempty"`
	// InstallDirSource 记录上述模板的依据：官方文档链接或本机 --help 实测。
	InstallDirSource string  `yaml:"install_dir_source,omitempty" json:"install_dir_source,omitempty"`
	Verify           *Verify `yaml:"verify,omitempty" json:"verify,omitempty"`
	ExpectReboot     bool    `yaml:"expect_reboot" json:"expect_reboot"`
	Source           string  `yaml:"source" json:"source"`
	VerifiedAt       *string `yaml:"verified_at,omitempty" json:"verified_at,omitempty"`
}

// CondaEnv 是一个 conda 环境的只读快照。
//
// Manager / CondaExe 记录它来自哪个 conda 安装；同机多个 conda 安装时，
// 同名环境也能看出各自的来源。Prefix 是扫描时的真实路径，导出时会被
// ${USER}/${HOST} 脱敏。
type CondaEnv struct {
	Name          string `yaml:"name" json:"name"`
	Prefix        string `yaml:"prefix" json:"prefix"`
	Manager       string `yaml:"manager,omitempty" json:"manager,omitempty"`
	CondaExe      string `yaml:"conda_exe,omitempty" json:"conda_exe,omitempty"`
	CondaVersion  string `yaml:"conda_version,omitempty" json:"conda_version,omitempty"`
	PythonVersion string `yaml:"python_version,omitempty" json:"python_version,omitempty"`
	// Base 表示这是 conda 的 base 环境。默认不进入可恢复列表。
	Base bool `yaml:"base,omitempty" json:"base,omitempty"`
	// RestoreConda / RestorePip 是 GUI 侧开关；nil 表示按默认 false 处理。
	RestoreConda *bool `yaml:"restore_conda,omitempty" json:"restore_conda,omitempty"`
	RestorePip   *bool `yaml:"restore_pip,omitempty" json:"restore_pip,omitempty"`
	// Mirror 引用 mirrors: 里的 profile 名；空表示直连默认源。
	Mirror      string         `yaml:"mirror,omitempty" json:"mirror,omitempty"`
	Packages    []CondaPackage `yaml:"packages,omitempty" json:"packages,omitempty"`
	PipPackages []PipPackage   `yaml:"pip_packages,omitempty" json:"pip_packages,omitempty"`
}

// CondaEnabled 报告是否恢复 conda 包（缺省 false）。
func (e CondaEnv) CondaEnabled() bool {
	return e.RestoreConda != nil && *e.RestoreConda
}

// PipEnabled 报告是否恢复 pip 包（缺省 false）。
func (e CondaEnv) PipEnabled() bool {
	return e.RestorePip != nil && *e.RestorePip
}

// CondaPackage 是 conda list 的一条记录。
type CondaPackage struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
	Build   string `yaml:"build,omitempty" json:"build,omitempty"`
	Channel string `yaml:"channel,omitempty" json:"channel,omitempty"`
}

// PipPackage 是 pip list 的一条记录。
type PipPackage struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version" json:"version"`
}

// MirrorProfile 是命名镜像配置。只保存源地址，不保存任何凭据。
type MirrorProfile struct {
	CondaChannels  []string `yaml:"conda_channels,omitempty" json:"conda_channels,omitempty"`
	PipIndexURL    string   `yaml:"pip_index_url,omitempty" json:"pip_index_url,omitempty"`
	PipExtraIndex  []string `yaml:"pip_extra_index_url,omitempty" json:"pip_extra_index_url,omitempty"`
	PipTrustedHost []string `yaml:"pip_trusted_host,omitempty" json:"pip_trusted_host,omitempty"`
}

// Verify 描述「如何判断该软件已经装好」，用于 restore 幂等判断（D13-P1/P3）。
type Verify struct {
	Command        string `yaml:"command" json:"command"`
	ExpectContains string `yaml:"expect_contains,omitempty" json:"expect_contains,omitempty"`
}

// 配方来源取值。
const (
	InstallerBuiltin = "builtin"
	InstallerUser    = "user"
)

// Validate 校验配置的结构合法性。scan 在写盘前必须调用。
func (c *Config) Validate() error { return c.validate() }

func (c *Config) validate() error {
	if c.App != AppName {
		return fmt.Errorf("配置 app 字段应为 %q，实际为 %q", AppName, c.App)
	}
	if c.Schema <= 0 {
		return fmt.Errorf("配置 schema 字段无效: %d", c.Schema)
	}
	if c.Schema > CurrentSchema {
		return fmt.Errorf("%w：配置 schema=%d，本程序支持到 %d，请升级 envlift",
			ErrSchemaTooNew, c.Schema, CurrentSchema)
	}
	seenRecipe := make(map[string]bool, len(c.Installers))
	for i, ins := range c.Installers {
		if ins.Name == "" {
			return fmt.Errorf("installers[%d].name 不能为空", i)
		}
		if ins.MatchDisplay != "" {
			if _, err := regexp.Compile(ins.MatchDisplay); err != nil {
				return fmt.Errorf("installers[%d](%s).match_display 不是合法正则: %w", i, ins.Name, err)
			}
		}
		// 重名配方会让「哪个配方对应哪个包」变得不确定，必须在配置层面就拒绝。
		key := strings.ToLower(ins.Name)
		if seenRecipe[key] {
			return fmt.Errorf("installers 中配方名重复: %q（第 %d 条）——重名会导致无法确定该用哪个配方", ins.Name, i)
		}
		seenRecipe[key] = true
		if ins.Match == "" {
			return fmt.Errorf("installers[%d](%s).match 不能为空", i, ins.Name)
		}
		switch ins.Source {
		case InstallerBuiltin, InstallerUser:
		default:
			return fmt.Errorf("installers[%d](%s).source 取值无效: %q，应为 builtin 或 user",
				i, ins.Name, ins.Source)
		}
		if err := validateInstallDirTemplate(ins.InstallDirArg); err != "" {
			return fmt.Errorf("installers[%d](%s).install_dir_arg %s", i, ins.Name, err)
		}
		if err := validateInstallDirTemplate(ins.WingetLocationArg); err != "" {
			return fmt.Errorf("installers[%d](%s).winget_location_arg %s", i, ins.Name, err)
		}
		if (ins.InstallDirArg != "" || ins.WingetLocationArg != "") &&
			strings.TrimSpace(ins.InstallDirSource) == "" {
			return fmt.Errorf("installers[%d](%s).install_dir_source 不能为空（已声明 install_dir_arg 或 winget_location_arg）",
				i, ins.Name)
		}
	}
	for i, env := range c.CondaEnvs {
		if strings.TrimSpace(env.Name) == "" {
			return fmt.Errorf("conda_envs[%d].name 不能为空", i)
		}
		if strings.TrimSpace(env.Prefix) == "" {
			return fmt.Errorf("conda_envs[%d](%s).prefix 不能为空", i, env.Name)
		}
		if env.Mirror != "" {
			if _, ok := c.Mirrors[env.Mirror]; !ok {
				return fmt.Errorf("conda_envs[%d](%s).mirror 引用了不存在的 profile %q",
					i, env.Name, env.Mirror)
			}
		}
		for j, p := range env.Packages {
			if strings.TrimSpace(p.Name) == "" {
				return fmt.Errorf("conda_envs[%d](%s).packages[%d].name 不能为空", i, env.Name, j)
			}
		}
		for j, p := range env.PipPackages {
			if strings.TrimSpace(p.Name) == "" {
				return fmt.Errorf("conda_envs[%d](%s).pip_packages[%d].name 不能为空", i, env.Name, j)
			}
		}
	}
	for name, profile := range c.Mirrors {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("mirrors 的 profile 名不能为空")
		}
		if err := validateMirrorProfile(profile); err != nil {
			return fmt.Errorf("mirrors[%s] %s", name, err)
		}
	}
	return nil
}

// validateMirrorProfile 拒绝把凭据写进配置文件，也拒绝静默走 HTTP。
func validateMirrorProfile(p MirrorProfile) error {
	for _, ch := range p.CondaChannels {
		ch = strings.TrimSpace(ch)
		if ch == "" {
			return fmt.Errorf("conda_channels 不能包含空值")
		}
		if err := validateMirrorURL(ch, nil, false); err != nil {
			return fmt.Errorf("conda channel %q: %w", ch, err)
		}
	}
	if p.PipIndexURL != "" {
		if err := validateMirrorURL(p.PipIndexURL, p.PipTrustedHost, true); err != nil {
			return fmt.Errorf("pip_index_url: %w", err)
		}
	}
	for _, u := range p.PipExtraIndex {
		if err := validateMirrorURL(u, p.PipTrustedHost, true); err != nil {
			return fmt.Errorf("pip_extra_index_url %q: %w", u, err)
		}
	}
	return nil
}

// validateMirrorURL 检查协议、凭据与显式 trusted-host。
//
// 非 URL 的 conda channel 名（如 conda-forge / defaults）直接放行；
// 任何带 userinfo 或敏感查询参数的 URL 都拒绝，避免把 token 写进 YAML。
func validateMirrorURL(raw string, trustedHosts []string, allowHTTPWithTrust bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("不能为空")
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "git+") {
		return fmt.Errorf("不允许 git+ 源")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		// conda channel 允许是命名 channel，不当 URL 处理。
		return nil
	}
	if u.User != nil {
		return fmt.Errorf("不允许在 URL 中保存用户名/密码/token")
	}
	for key := range u.Query() {
		if sensitiveQueryKey(key) {
			return fmt.Errorf("URL 查询参数 %q 看起来包含凭据，拒绝保存", key)
		}
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if allowHTTPWithTrust && trustedHost(u.Hostname(), trustedHosts) {
			return nil
		}
		return fmt.Errorf("HTTP 源必须显式加入 pip_trusted_host；默认只允许 HTTPS")
	case "file":
		return nil
	default:
		return fmt.Errorf("不支持的协议 %q", u.Scheme)
	}
}

func trustedHost(host string, list []string) bool {
	for _, h := range list {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return true
		}
	}
	return false
}

func sensitiveQueryKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "token", "access_token", "auth", "apikey", "api_key", "key", "password", "passwd", "secret":
		return true
	}
	return strings.Contains(k, "token") || strings.Contains(k, "password")
}

// validateInstallDirTemplate 校验安装目录模板，返回空串表示合法。
//
// 模板会直接拼进命令行，所以控制字符、未配对引号必须在加载配置时就拒绝；
// {install_dir} 出现多次会让「替换一次」的语义变得不确定，也一并拒绝。
func validateInstallDirTemplate(tpl string) string {
	if tpl == "" {
		return ""
	}
	if n := strings.Count(tpl, "{install_dir}"); n != 1 {
		return fmt.Sprintf("必须恰好包含一个 {install_dir}（实际 %d 个）", n)
	}
	for _, r := range tpl {
		if r == '\n' || r == '\r' || r < 0x20 || r == 0x7f {
			return "不得含换行或控制字符"
		}
	}
	if strings.Count(tpl, `"`)%2 != 0 {
		return "不得含未配对的双引号"
	}
	return ""
}

// StringPtr 返回 s 的指针，用于表达 schema 中的可空字符串字段。
func StringPtr(s string) *string { return &s }

// IntPtr 返回 n 的指针，用于表达 schema 中的可空整数字段。
func IntPtr(n int) *int { return &n }

// BoolPtr 返回 b 的指针，用于表达 schema 中的可空布尔字段。
func BoolPtr(b bool) *bool { return &b }
