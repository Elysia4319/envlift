// Package plan 生成还原计划。
//
// 两条硬约束：
//   - C1：dry-run 与 --apply 必须共用同一个 Build，禁止「打印一套、执行一套」。
//   - C2：Build 是纯函数——入参全部是数据，不读注册表、不跑 winget、不读目录。
//
// 计划本身是可序列化结构体，reports 的 .json 就是它的产物。
package plan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/match"
	"github.com/Elysia4319/envlift/internal/relocation"
	"github.com/Elysia4319/envlift/internal/targetid"
	"github.com/Elysia4319/envlift/internal/vcompare"
)

// 层级（D3）。
const (
	TierWinget = "winget"
	TierLocal  = "local"
	TierManual = "manual"
)

// 条目状态。
const (
	StatusPlanned   = "planned"
	StatusSkipped   = "skipped"
	StatusManual    = "manual"
	StatusAmbiguous = "needs-disambiguation"
	// StatusCovered 表示同一配方的多个条目里去重被覆盖掉的那些（A1/A5）。
	StatusCovered = "covered"
	// StatusBlockedNeedsDecision 表示该条目还没有决策（C2）：跳过执行、进报告，
	// 但不阻断整批——它不是错误，是「等人拍板」。
	StatusBlockedNeedsDecision = "blocked-needs-decision"
)

// PATH 变更标记（C9）。
const (
	MarkAdd     = "+"
	MarkRewrite = "~"
	MarkPending = "?"
	MarkSkip    = "="
	MarkInvalid = "!"
)

// PathStatusSkipped 表示这条 PATH 变更被决策明确排除（skip / mark_invalid），
// 不会写进注册表。
const PathStatusSkipped = "skipped"

// RuntimeState 是目标机上某个运行时的现状。
type RuntimeState struct {
	Present bool   `json:"present"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
}

// Target 是目标机环境探测结果。dry-run 时就是本机。
type Target struct {
	OS        string `json:"os"`
	Build     string `json:"build"`
	Arch      string `json:"arch"`
	Username  string `json:"username"`
	Hostname  string `json:"hostname"`
	Elevated  bool   `json:"elevated"`
	WingetOK  bool   `json:"winget_ok"`
	WingetVer string `json:"winget_version,omitempty"`
	// WingetForceOK 表示本机 winget 的 install 子命令支持 --force。
	WingetForceOK bool `json:"winget_force_ok"`
	NetworkOK     bool `json:"network_ok"`
	// NetworkNote 说明网络判定的依据（探测失败时写原因）。
	NetworkNote string `json:"network_note,omitempty"`
	// Runtimes 以运行时名为键。
	Runtimes map[string]RuntimeState `json:"runtimes"`
	// PathDirs 是目标机当前 PATH 的展开值，用于比对。
	PathDirs []string `json:"path_dirs"`
	// Drives 是目标机盘符，形如 "C:"。
	Drives []string `json:"drives"`
}

// Options 是 Build 的全部输入。没有任何外部 IO。
type Options struct {
	Config *config.Config
	Target Target
	// InstallerDir 是 installers/ 目录的绝对路径。
	InstallerDir string
	// InstallerFiles 是该目录下的文件名（不含路径）。
	InstallerFiles []string
	// Recipes 是已合并的配方表（user 覆盖 builtin）。
	Recipes []config.Installer
	Now     time.Time
	// Exists 判断目标机上路径是否存在。
	Exists func(string) bool
	// Expand 把 ${USER}/${HOST} 换成目标机的真实值。
	Expand func(string) string
	// ExpandVars 额外展开 %VAR%，**只用于存在性检查**，不影响展示值（B1）。
	ExpandVars func(string) string
	// RawConfig 由调用方在代入变量之前判定，见 DetectRaw。
	RawConfig bool
	// Decisions 是本机侧的选择（dry-run 与 apply 必须用同一份）。
	// 缺省为空 = 全部按默认处理，未决策项标 blocked-needs-decision。
	Decisions Decisions
	// Relocations 是本机重装账本；命中时 set_location 不再重复重装。
	Relocations *relocation.Ledger
}

// DetectRaw 判定配置是否为 --raw 导出。
//
// 必须在 Restore 之前调用：一旦占位符被代入成真实值，就再也分不出来了。
func DetectRaw(cfg *config.Config) bool { return !config.HasPlaceholders(cfg) }

// FallbackPlan 是 winget 失败后可执行的本地包降级方案（D7）。
//
// 只存执行所需的字段，避免把整个 Item 复制进 JSON；TargetID / Name 与父项一致。
type FallbackPlan struct {
	Tier          string `json:"tier"`
	Command       string `json:"command"`
	InstallDir    string `json:"install_dir,omitempty"`
	NeedsAdmin    bool   `json:"needs_admin"`
	ExpectReboot  bool   `json:"expect_reboot"`
	VerifyCmd     string `json:"verify_command,omitempty"`
	VerifyExpect  string `json:"verify_expect_contains,omitempty"`
	RecipeName    string `json:"recipe,omitempty"`
	RecipeSource  string `json:"recipe_source,omitempty"`
	InstallerFile string `json:"installer_file,omitempty"`
}

// Item 是计划中的一项。
type Item struct {
	// TargetID 是**稳定标识**（targetid）：④ 的决策靠它引用条目。
	//
	// 不能用显示名（真实机器上同名条目很多）也不能用数组下标（决策会让 plan
	// 重算，重算后顺序可能变）。它由条目内容算出，不落盘、不进 schema。
	TargetID  string `json:"target_id"`
	Kind      string `json:"kind"` // software | runtime
	Name      string `json:"name"`
	Publisher string `json:"publisher,omitempty"`
	Location  string `json:"install_location,omitempty"`
	Expected  string `json:"expected_version,omitempty"`
	Current   string `json:"current_version,omitempty"`
	Tier      string `json:"tier,omitempty"`
	Status    string `json:"status"`
	// Decision 记录本条最终采用的决策动作（④）。前端据此显示徽章、允许撤销；
	// 空表示没有下过决策。
	Decision string `json:"decision,omitempty"`
	// Candidates / CandidateKind 是「决策时可选的值」：多版本（version）或
	// 多个安装包（installer）。为空表示该条目没有可选项，select 对它无意义。
	Candidates    []string `json:"candidates,omitempty"`
	CandidateKind string   `json:"candidate_kind,omitempty"`
	Command       string   `json:"command,omitempty"`
	NeedsAdmin    bool     `json:"needs_admin"`
	ExpectReboot  bool     `json:"expect_reboot"`
	VerifyCmd     string   `json:"verify_command,omitempty"`
	VerifyExpect  string   `json:"verify_expect_contains,omitempty"`
	Fallback      string   `json:"fallback,omitempty"`
	// FallbackPlan 是可直接执行的本地包降级方案；只有 winget 命中本地包时才有。
	FallbackPlan *FallbackPlan `json:"fallback_plan,omitempty"`
	Reason       string        `json:"reason,omitempty"`
	MovedBy      string        `json:"reordered_reason,omitempty"`
	ManualHint   string        `json:"manual_hint,omitempty"`
	RecipeName   string        `json:"recipe,omitempty"`
	RecipeSource string        `json:"recipe_source,omitempty"`
	VerifiedAt   string        `json:"recipe_verified_at,omitempty"`
	// MatchedBy 记录配方是靠哪一级锚点命中的（M5）。
	// 正则匹配错了极难排查，没有这个字段等于放弃调试。
	MatchedBy     string `json:"matched_by"`
	InstallerFile string `json:"installer_file,omitempty"`
	// InstallDir / InstallDirApplied 是用户通过 set_location 决策指定的安装目录。
	// 它们不进 target_id（D23）：身份仍由软件本身决定，目录只是本次执行参数。
	InstallDir        string `json:"install_dir,omitempty"`
	InstallDirApplied bool   `json:"install_dir_applied,omitempty"`
	// InstallDirActual 是账本里记录的上次实际目录，只作展示，不参与跳过判断。
	InstallDirActual string `json:"install_dir_actual,omitempty"`
	// InstallDirVerified 在 plan 阶段表示账本里的上次验证结果；apply 后会以本次 Step 结果覆盖。
	InstallDirVerified bool `json:"install_dir_verified,omitempty"`
	// Reinstall 表示本次即使 verify 通过也要执行安装（指定目录后的重装语义）。
	Reinstall bool `json:"reinstall,omitempty"`
	// InstallDirSupported 表示该条目命中的配方是否声明了安装目录模板。
	InstallDirSupported bool `json:"install_dir_supported,omitempty"`
	// 以下三个字段供去重选代表与报告展示（A2）。
	Hive     string `json:"hive,omitempty"`
	ArchView string `json:"arch_view,omitempty"`
	Pinned   bool   `json:"pinned,omitempty"`
}

// PathChange 是一条 PATH 变更计划。
type PathChange struct {
	// TargetID 同 Item.TargetID，PATH 待确认项的决策靠它引用。
	TargetID string `json:"target_id"`
	Section  string `json:"section"` // user | machine
	Mark     string `json:"mark"`
	Value    string `json:"value"`
	Reason   string `json:"reason,omitempty"`
	// Decision / Status 与 Item 同义：本条用了什么决策；未决策时为
	// blocked-needs-decision，被决策跳过时为 skipped。
	Decision string `json:"decision,omitempty"`
	Status   string `json:"status,omitempty"`
}

// Preflight 是环境预检结果。
type Preflight struct {
	InstallerFiles   int `json:"installer_files"`
	InstallerMatched int `json:"installer_matched"`
	Recipes          int `json:"recipes"`
	SoftwareTotal    int `json:"software_total"`
	SoftwareMatched  int `json:"software_matched"`
}

// Summary 是汇总计数。
type Summary struct {
	Total     int `json:"total"`
	Winget    int `json:"winget"`
	Local     int `json:"local"`
	Manual    int `json:"manual"`
	Skipped   int `json:"skipped"`
	Pending   int `json:"pending_confirm"`
	Ambiguous int `json:"ambiguous"`
	// BlockedNeedsDecision 是未决策（C2）而被跳过的条目数；它不阻断执行。
	BlockedNeedsDecision int `json:"blocked_needs_decision"`
	// ByDecision 是按决策改变处理方式的条目数（skip / mark_manual / 改选代表等）。
	ByDecision int `json:"by_decision"`
}

// Plan 是完整的还原计划。
type Plan struct {
	App         string       `json:"app"`
	Kind        string       `json:"kind"`
	Schema      int          `json:"schema"`
	GeneratedAt string       `json:"generated_at"`
	RawConfig   bool         `json:"raw_config"`
	Target      Target       `json:"target"`
	Preflight   Preflight    `json:"preflight"`
	Items       []Item       `json:"items"`
	Path        []PathChange `json:"path_changes"`
	Warnings    []string     `json:"warnings,omitempty"`
	Summary     Summary      `json:"summary"`
	// Blocked 非空表示计划不可执行（有环），此时不产出可执行计划（C10）。
	Blocked string `json:"blocked,omitempty"`

	// 以下字段不序列化，只在构建期服务决策：
	//   baseByID        去重前的原始条目（改选代表要恢复它）
	//   excludedTargets 扫描时被排除导出的条目 id（不允许对它们下决策）
	//   resolvers       选中安装包后按配方重算命令的构造函数
	baseByID        map[string]Item
	excludedTargets map[string]bool
	resolvers       map[string]func(Item, string) Item
	// recipeResolvers 是「只用某个配方重算条目」的构造函数（A2 的 select{recipe}）。
	recipeResolvers map[string]func(Item, string) Item
	// locationResolvers 按「条目 + 安装目录」重算命令；决策落盘时使用。
	locationResolvers map[string]func(Item, string) Item
	// recipeInput 是构建时的配方表，校验 set_location 时用来查模板。
	recipeInput []config.Installer
	// relocations 是本机重装账本；只在构建期用于一次性重装判断。
	relocations *relocation.Ledger
}

// IsBlocked 报告计划是否因硬错误不可执行。
func (p *Plan) IsBlocked() bool { return p.Blocked != "" }

type candidate struct {
	recipeIdx int
	file      string
	// extraFiles 非空表示该配方在 installers/ 里命中多个安装包，
	// 无法判断该装哪一个，必须交给人决定，不许取第一个了事。
	extraFiles []string
}

// Build 生成计划。纯函数：同样的入参必然得到同样的结果。
func Build(opts Options) (*Plan, error) {
	cfg := opts.Config
	if cfg == nil {
		return nil, fmt.Errorf("配置为空")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if opts.Exists == nil {
		opts.Exists = func(string) bool { return false }
	}
	if opts.Expand == nil {
		opts.Expand = func(s string) string { return s }
	}
	if opts.ExpandVars == nil {
		opts.ExpandVars = func(s string) string { return s }
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	matcher, err := match.NewMatcher(opts.Recipes)
	if err != nil {
		return nil, err
	}

	p := &Plan{
		App:         config.AppName,
		Kind:        "restore-plan",
		Schema:      config.CurrentSchema,
		GeneratedAt: now.Format(time.RFC3339),
		Target:      opts.Target,
		// C8：是否 --raw 由调用方在代入变量前判定后传入。
		RawConfig:         opts.RawConfig,
		baseByID:          map[string]Item{},
		excludedTargets:   excludedIDSet(cfg),
		resolvers:         map[string]func(Item, string) Item{},
		recipeResolvers:   map[string]func(Item, string) Item{},
		locationResolvers: map[string]func(Item, string) Item{},
		recipeInput:       opts.Recipes,
		relocations:       opts.Relocations,
	}

	byRecipe := matchInstallerFiles(opts.Recipes, opts.InstallerFiles)
	p.Preflight.Recipes = len(opts.Recipes)
	p.Preflight.InstallerFiles = len(opts.InstallerFiles)
	matched := 0
	for _, files := range byRecipe {
		matched += len(files)
	}
	p.Preflight.InstallerMatched = matched
	p.Preflight.SoftwareTotal = len(cfg.Software)

	// A3：id 只在三源合并之后、这一处生成；合并前的中间态不持有 id。
	items := make([]Item, 0, len(cfg.Software)+len(cfg.Runtimes))
	items = append(items, buildSoftwareItems(cfg, opts, matcher, byRecipe, p)...)
	items = append(items, buildRuntimeItems(cfg, opts, matcher, byRecipe, p)...)

	// dedupe 会抹掉被覆盖条目的层级与命令，而 ④ 的「改选代表」要把它恢复回来，
	// 所以先留一份去重前的快照。
	for _, it := range items {
		p.baseByID[it.TargetID] = it
	}
	assignVersionCandidates(items, opts.Recipes)

	// A1/A5：同一配方的多个条目按 multi_version 决定是去重还是各自独立。
	// 决策里指定了代表的，按决策选，不按 A2 的优先级猜。
	dedupeByRecipe(items, opts.Recipes, preferredByDecisions(items, opts.Decisions))

	ordered, err := orderItems(items, opts.Recipes)
	if err != nil {
		p.Blocked = err.Error()
		p.Items = items
		p.Path = buildPathChanges(cfg, opts)
		ApplyDecisions(p, opts.Decisions)
		summarize(p)
		return p, nil
	}
	p.Items = ordered
	p.Path = buildPathChanges(cfg, opts)

	// B4：未知 target_id / kind / action、参数不合法、引用已排除条目——一律报错，
	// 绝不「加载了但没生效」。
	if errs := ValidateDecisions(p, opts.Decisions); errs != nil {
		return nil, errs
	}
	ApplyDecisions(p, opts.Decisions)
	summarize(p)
	return p, nil
}

// expandPtr 把可空字符串字段代入 ${USER}/${HOST}；nil 保持 nil。
func expandPtr(opts Options, p *string) *string {
	if p == nil {
		return nil
	}
	v := opts.Expand(*p)
	return &v
}

// softwareForID 返回「代入之后」的副本，**仅用于算 id**（A1-b）。
//
// 不这么做的话，同一份配置在「带占位符的 GUI 路径」与「CLI 已 Restore 的配置」
// 上会算出两个 id：排除导出引用一个、计划条目引用另一个，且两边都不报错。
func softwareForID(opts Options, sw config.Software) config.Software {
	out := sw
	out.Name = opts.Expand(sw.Name)
	out.Publisher = expandPtr(opts, sw.Publisher)
	out.Version = expandPtr(opts, sw.Version)
	out.InstallLocation = expandPtr(opts, sw.InstallLocation)
	return out
}

// runtimeForID 同 softwareForID。
func runtimeForID(opts Options, rt config.Runtime) config.Runtime {
	out := rt
	out.Name = opts.Expand(rt.Name)
	out.Version = expandPtr(opts, rt.Version)
	out.Manager = expandPtr(opts, rt.Manager)
	out.Executable = expandPtr(opts, rt.Executable)
	return out
}

// excludedIDSet 收集扫描时被排除导出的条目 id（x-excluded）。
func excludedIDSet(cfg *config.Config) map[string]bool {
	out := map[string]bool{}
	for _, sw := range cfg.Excluded() {
		out[targetid.Software(sw)] = true
	}
	return out
}

// assignVersionCandidates 给「同一配方下有多条不同版本」的条目挂上版本候选。
//
// 只有会被去重的组（multi_version=false）才有意义：改选其中一条 = 本次只装这个
// 版本。multi_version=true 的产品本来就要各版本全装，没有「选哪个」这回事。
func assignVersionCandidates(items []Item, recipes []config.Installer) {
	groups := map[string][]int{}
	var order []string
	for i, it := range items {
		if it.RecipeName == "" {
			continue
		}
		if _, ok := groups[it.RecipeName]; !ok {
			order = append(order, it.RecipeName)
		}
		groups[it.RecipeName] = append(groups[it.RecipeName], i)
	}
	for _, name := range order {
		idxs := groups[name]
		if len(idxs) < 2 {
			continue
		}
		if r := findRecipe(recipes, name); r != nil && r.MultiVersion {
			continue
		}
		var vers []string
		seen := map[string]bool{}
		for _, i := range idxs {
			v := strings.TrimSpace(items[i].Expected)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			vers = append(vers, v)
		}
		if len(vers) < 2 {
			continue
		}
		for _, i := range idxs {
			if items[i].CandidateKind != "" {
				continue // 安装包歧义的候选更具体，不覆盖
			}
			items[i].Candidates = vers
			items[i].CandidateKind = CandidateVersion
		}
	}
}

// preferredByDecisions 算出「决策要求当代表」的条目 id。
//
// override_representative 直接把目标条目提为代表；select{version} 指向的是组里
// 的某一条版本，所以要在同配方组内按版本找到那一条。
func preferredByDecisions(items []Item, ds Decisions) map[string]bool {
	out := map[string]bool{}
	for _, d := range ds {
		switch {
		case d.Action == ActionOverrideRepresentative:
			out[d.TargetID] = true
		case d.Action == ActionSelect && d.Params != nil && d.Params.Version != nil:
			want := strings.TrimSpace(*d.Params.Version)
			recipe := ""
			for _, it := range items {
				if it.TargetID == d.TargetID {
					recipe = it.RecipeName
					break
				}
			}
			if recipe == "" {
				continue
			}
			for _, it := range items {
				if strings.EqualFold(it.RecipeName, recipe) &&
					strings.EqualFold(strings.TrimSpace(it.Expected), want) {
					out[it.TargetID] = true
				}
			}
		}
	}
	return out
}

func matchInstallerFiles(recipes []config.Installer, files []string) map[int][]string {
	out := make(map[int][]string, len(recipes))
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	for i, r := range recipes {
		for _, f := range sorted {
			ok, err := filepath.Match(strings.ToLower(r.Match), strings.ToLower(f))
			if err != nil || !ok {
				continue
			}
			out[i] = append(out[i], f)
		}
	}
	return out
}

// localCandidate 把「配方」和「installers/ 里匹配到的文件」组合起来。
func localCandidate(recipeIdx int, byRecipe map[int][]string) candidate {
	if recipeIdx < 0 {
		return candidate{recipeIdx: -1}
	}
	files := byRecipe[recipeIdx]
	if len(files) == 0 {
		return candidate{recipeIdx: recipeIdx}
	}
	c := candidate{recipeIdx: recipeIdx, file: files[0]}
	if len(files) > 1 {
		c.extraFiles = files[1:]
	}
	return c
}

func buildSoftwareItems(cfg *config.Config, opts Options, m *match.Matcher, byRecipe map[int][]string, p *Plan) []Item {
	items := make([]Item, 0, len(cfg.Software))
	for _, sw := range cfg.Software {
		it := Item{
			TargetID: targetid.Software(softwareForID(opts, sw)),
			Kind:     "software", Name: sw.Name, Status: StatusPlanned, MatchedBy: match.BasisNone,
		}
		if sw.Version != nil {
			it.Expected = *sw.Version
		}
		if sw.Publisher != nil {
			it.Publisher = *sw.Publisher
		}
		if sw.InstallLocation != nil {
			it.Location = *sw.InstallLocation
		}
		if sw.ManualHint != nil {
			it.ManualHint = *sw.ManualHint
		}
		if sw.Hive != nil {
			it.Hive = *sw.Hive
		}
		if sw.ArchView != nil {
			it.ArchView = *sw.ArchView
		}
		if sw.Pinned != nil {
			it.Pinned = *sw.Pinned
		}

		idx, basis, all := m.Find(sw.Name, sw.WingetID)
		it.MatchedBy = basis
		if idx >= 0 {
			it.RecipeName = opts.Recipes[idx].Name
			p.Preflight.SoftwareMatched++
		}

		cand := localCandidate(idx, byRecipe)
		hasLocal := idx >= 0 && cand.file != "" && opts.Recipes[idx].SilentArgs != ""

		switch {
		// M4：多个不同配方命中同一条目，必须暴露，不猜。
		case len(all) > 1:
			it.Status = StatusAmbiguous
			it.RecipeName = ""
			it.Reason = fmt.Sprintf("%s 级命中 %d 个配方（%s），需人工指定",
				basis, len(all), describeCandidates(all))
			it.Candidates = recipeNames(all)
			it.CandidateKind = CandidateRecipe
			p.recipeResolvers[it.TargetID] = p.recipeResolver(opts, byRecipe)

		case opts.Target.WingetOK && opts.Target.NetworkOK && sw.WingetID != nil:
			it.Tier = TierWinget
			// winget 安装器多为机器级安装，保守标记为需要提权。
			// 不标的话 C-2 的 admin_confirmed 就形同虚设——实测就是这么绕过去的。
			it.NeedsAdmin = true
			it.Command = buildWingetCommand(*sw.WingetID, "")
			// 配方命中时，verify / expect_reboot 与安装层级无关：winget 装完也要
			// 用同一套校验判断是否已完成，否则重跑会再次执行安装并因「已安装」失败。
			if idx >= 0 {
				r := opts.Recipes[idx]
				it.RecipeSource = r.Source
				if r.VerifiedAt != nil {
					it.VerifiedAt = *r.VerifiedAt
				}
				if r.Verify != nil {
					it.VerifyCmd = opts.Expand(r.Verify.Command)
					it.VerifyExpect = r.Verify.ExpectContains
				}
				it.ExpectReboot = r.ExpectReboot
			}
			it.InstallDirSupported = idx >= 0 && opts.Recipes[idx].WingetLocationArg != ""
			if hasLocal {
				fb := it
				fb.Tier = TierLocal
				fb.Command = ""
				fb.Fallback = ""
				fb.FallbackPlan = nil
				fillLocal(&fb, opts, cand)
				it.FallbackPlan = &FallbackPlan{
					Tier:          fb.Tier,
					Command:       fb.Command,
					InstallDir:    fb.InstallDir,
					NeedsAdmin:    fb.NeedsAdmin,
					ExpectReboot:  fb.ExpectReboot,
					VerifyCmd:     fb.VerifyCmd,
					VerifyExpect:  fb.VerifyExpect,
					RecipeName:    fb.RecipeName,
					RecipeSource:  fb.RecipeSource,
					InstallerFile: fb.InstallerFile,
				}
				it.Fallback = "失败后降级到本地包 " + cand.file
			} else {
				it.Fallback = "失败后标 manual（installers/ 中无匹配安装包）"
			}
			p.locationResolvers[it.TargetID] = func(old Item, dir string) Item {
				out := old
				r := opts.Recipes[idx]
				out.InstallDir = dir
				out.InstallDirApplied = dir != ""
				extra := applyInstallDir(r.WingetLocationArg, dir)
				if opts.Target.WingetForceOK {
					extra = strings.TrimSpace(extra + " --force")
				}
				out.Command = buildWingetCommand(*sw.WingetID, extra)
				if out.FallbackPlan != nil {
					fb := *out.FallbackPlan
					fb.InstallDir = dir
					if cmd, applied := localCommandWithDir(opts, cand, dir); applied {
						fb.Command = cmd
						out.FallbackPlan = &fb
					} else {
						out.FallbackPlan = nil
						out.Fallback = "指定目录时不可降级到本地包（配方未声明本地目录参数）"
					}
				}
				return out
			}

		case hasLocal && len(cand.extraFiles) > 0:
			it.Status = StatusAmbiguous
			it.Reason = fmt.Sprintf("配方 %s 命中 %d 个安装包（%s），需人工指定装哪一个",
				opts.Recipes[idx].Name, len(cand.extraFiles)+1,
				strings.Join(append([]string{cand.file}, cand.extraFiles...), ", "))
			it.Candidates = append([]string{cand.file}, cand.extraFiles...)
			it.CandidateKind = CandidateInstaller
			p.resolvers[it.TargetID] = installerResolver(opts, idx)

		case hasLocal:
			it.Tier = TierLocal
			fillLocal(&it, opts, cand)
			it.InstallDirSupported = opts.Recipes[idx].InstallDirArg != ""
			it.Fallback = "失败即停止并保留已装部分（D11）"
			p.locationResolvers[it.TargetID] = localLocationResolver(opts, cand)
			if sw.WingetID != nil {
				it.Reason = wingetSkipReason(opts)
			}

		// 已识别配方但包里没有对应安装文件：仍算「识别到了」，只是没法自动装。
		case idx >= 0:
			it.Tier = TierManual
			it.Status = StatusManual
			it.Reason = fmt.Sprintf("已识别配方 %s，但 installers/ 中无匹配安装包", opts.Recipes[idx].Name)
			it.Fallback = "无自动路径"

		default:
			it.Tier = TierManual
			it.Status = StatusManual
			it.Reason = manualReason(sw, opts)
			it.Fallback = "无自动路径"
		}

		items = append(items, it)
	}
	return items
}

func buildRuntimeItems(cfg *config.Config, opts Options, m *match.Matcher, byRecipe map[int][]string, p *Plan) []Item {
	items := make([]Item, 0, len(cfg.Runtimes))
	for _, rt := range cfg.Runtimes {
		it := Item{
			TargetID: targetid.Runtime(runtimeForID(opts, rt)),
			Kind:     "runtime", Name: rt.Name, Status: StatusPlanned, MatchedBy: match.BasisNone,
		}
		if rt.Version != nil {
			it.Expected = *rt.Version
		}
		state := opts.Target.Runtimes[rt.Name]
		it.Current = state.Version

		// P1 幂等：已装且版本一致就跳过，不重装也不报错。
		if state.Present {
			it.Status = StatusSkipped
			if it.Expected == "" || state.Version == it.Expected {
				it.Reason = "已存在，版本一致"
			} else {
				it.Reason = fmt.Sprintf("已存在但版本不同（期望 %s，实际 %s），版本比对只报差异不阻断",
					it.Expected, state.Version)
			}
			items = append(items, it)
			continue
		}

		// A6：版本管理器托管的运行时不能走包管理器装——fnm/nvm 下的 node 是 shim，
		// 用 winget 装一个同版本 node 得到的是完全不同的东西。标 manual 并给命令。
		if rt.Manager != nil && *rt.Manager != "" {
			it.Tier = TierManual
			it.Status = StatusManual
			it.Reason = fmt.Sprintf("由版本管理器 %s 托管，不走包管理器安装（A6）", *rt.Manager)
			it.ManualHint = managerHint(*rt.Manager, rt.Name, it.Expected)
			items = append(items, it)
			continue
		}

		idx, basis, all := m.Find(rt.Name, nil)
		it.MatchedBy = basis
		if idx >= 0 {
			it.RecipeName = opts.Recipes[idx].Name
		}
		cand := localCandidate(idx, byRecipe)
		hasLocal := idx >= 0 && cand.file != "" && opts.Recipes[idx].SilentArgs != ""

		switch {
		case len(all) > 1:
			it.Status = StatusAmbiguous
			it.RecipeName = ""
			it.Reason = fmt.Sprintf("%s 级命中 %d 个配方（%s），需人工指定",
				basis, len(all), describeCandidates(all))
			it.Candidates = recipeNames(all)
			it.CandidateKind = CandidateRecipe
			p.recipeResolvers[it.TargetID] = p.recipeResolver(opts, byRecipe)
		case hasLocal && len(cand.extraFiles) > 0:
			it.Status = StatusAmbiguous
			it.Reason = fmt.Sprintf("配方 %s 命中 %d 个安装包，需人工指定装哪一个",
				opts.Recipes[idx].Name, len(cand.extraFiles)+1)
			it.Candidates = append([]string{cand.file}, cand.extraFiles...)
			it.CandidateKind = CandidateInstaller
			p.resolvers[it.TargetID] = installerResolver(opts, idx)
		case hasLocal:
			it.Tier = TierLocal
			fillLocal(&it, opts, cand)
			it.InstallDirSupported = opts.Recipes[idx].InstallDirArg != ""
			it.Fallback = "失败即停止并保留已装部分（D11）"
			p.locationResolvers[it.TargetID] = localLocationResolver(opts, cand)
		case idx >= 0:
			it.Tier = TierManual
			it.Status = StatusManual
			it.Reason = fmt.Sprintf("已识别配方 %s，但 installers/ 中无匹配安装包", opts.Recipes[idx].Name)
			it.Fallback = "无自动路径"
		default:
			it.Tier = TierManual
			it.Status = StatusManual
			it.Reason = runtimeManualReason(rt)
			it.Fallback = "无自动路径"
		}
		items = append(items, it)
	}
	return items
}

// recipeNames 抽出候选配方名，作为「配方歧义」条目的候选列表。
func recipeNames(all []match.Candidate) []string {
	out := make([]string, 0, len(all))
	for _, c := range all {
		out = append(out, c.RecipeName)
	}
	return out
}

// installerResolver 返回「选中某个安装包后按配方重算该条目」的构造函数。
//
// 计划是纯数据，但「用哪个安装包」会决定命令、提权、重启与校验命令，所以必须
// 由配方重新算一遍，不能只改一个文件名字段——那会留下命令与文件不一致的条目。
func installerResolver(opts Options, recipeIdx int) func(Item, string) Item {
	return func(old Item, file string) Item {
		out := old
		out.Status = StatusPlanned
		out.Tier = TierLocal
		out.Command = ""
		fillLocal(&out, opts, candidate{recipeIdx: recipeIdx, file: file})
		out.Fallback = "失败即停止并保留已装部分（D11）"
		out.Candidates = nil
		out.CandidateKind = ""
		// 当前版本不支持 select{installer} 与 set_location 组合。
		out.InstallDirSupported = false
		return out
	}
}

func describeCandidates(all []match.Candidate) string {
	parts := make([]string, 0, len(all))
	for _, c := range all {
		parts = append(parts, c.RecipeName)
	}
	return strings.Join(parts, ", ")
}

// applyInstallDir 把模板里的 {install_dir} 替换为实际目录；空模板返回空串。
func applyInstallDir(template, dir string) string {
	if template == "" || dir == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{install_dir}", dir)
}

// buildWingetCommand 生成 winget 安装命令；extraArgs 为空时保持原有默认格式。
func buildWingetCommand(wingetID, extraArgs string) string {
	cmd := fmt.Sprintf(
		"winget install --id %s --exact --silent --accept-package-agreements --accept-source-agreements",
		wingetID)
	if extraArgs != "" {
		cmd += " " + extraArgs
	}
	return cmd
}

func fillLocal(it *Item, opts Options, c candidate) {
	fillLocalWithDir(it, opts, c, "")
}

func fillLocalWithDir(it *Item, opts Options, c candidate, dir string) {
	r := opts.Recipes[c.recipeIdx]
	path := filepath.Join(opts.InstallerDir, c.file)
	args := r.SilentArgs
	if extra := applyInstallDir(r.InstallDirArg, dir); extra != "" {
		args = strings.TrimSpace(args + " " + extra)
	}
	// C7：展示与执行的都是代入变量后的最终命令，不留占位符。
	it.Command = opts.Expand(fmt.Sprintf(`"%s" %s`, path, args))
	it.InstallDir = dir
	if dir != "" {
		it.InstallDirApplied = true
	}
	it.NeedsAdmin = r.NeedsAdmin
	it.ExpectReboot = r.ExpectReboot
	it.RecipeName = r.Name
	it.RecipeSource = r.Source
	it.InstallerFile = c.file
	if r.VerifiedAt != nil {
		it.VerifiedAt = *r.VerifiedAt
	}
	if r.Verify != nil {
		it.VerifyCmd = opts.Expand(r.Verify.Command)
		it.VerifyExpect = r.Verify.ExpectContains
	}
}

// localCommandWithDir 返回本地安装包在指定目录下的命令，以及目录是否真的应用了。
// dir 非空但配方没有本地目录参数时返回 ("", false)，调用方必须禁用降级。
func localCommandWithDir(opts Options, c candidate, dir string) (string, bool) {
	r := opts.Recipes[c.recipeIdx]
	if dir != "" && r.InstallDirArg == "" {
		return "", false
	}
	path := filepath.Join(opts.InstallerDir, c.file)
	args := r.SilentArgs
	if extra := applyInstallDir(r.InstallDirArg, dir); extra != "" {
		args = strings.TrimSpace(args + " " + extra)
	}
	return opts.Expand(fmt.Sprintf(`"%s" %s`, path, args)), true
}

// localLocationResolver 返回「本地包条目 + 安装目录」的命令重算闭包。
func localLocationResolver(opts Options, c candidate) func(Item, string) Item {
	return func(old Item, dir string) Item {
		out := old
		out.InstallDir = dir
		out.InstallDirApplied = dir != ""
		if cmd, applied := localCommandWithDir(opts, c, dir); applied {
			out.Command = cmd
		}
		return out
	}
}

func wingetSkipReason(opts Options) string {
	switch {
	case !opts.Target.WingetOK:
		return "winget 不可用，整级跳过（D7）"
	case !opts.Target.NetworkOK:
		return "网络不可达，winget 级整级跳过（D7）"
	default:
		return "无 winget 包映射"
	}
}

func manualReason(sw config.Software, opts Options) string {
	if sw.WingetID == nil {
		return "无 winget 包映射，且没有配方覆盖"
	}
	return wingetSkipReason(opts) + "，且没有配方覆盖"
}

// managerHint 给出版本管理器的安装命令，作为 manual 的人工步骤。
func managerHint(manager, name, version string) string {
	v := version
	if v == "" {
		v = "<version>"
	}
	switch strings.ToLower(manager) {
	case "fnm":
		return fmt.Sprintf("fnm install %s && fnm default %s", v, v)
	case "nvm":
		return fmt.Sprintf("nvm install %s && nvm use %s", v, v)
	case "pyenv":
		return fmt.Sprintf("pyenv install %s && pyenv global %s", v, v)
	case "conda":
		return fmt.Sprintf("conda create -n %s python=%s", name, v)
	case "rustup":
		return fmt.Sprintf("rustup toolchain install %s", v)
	case "sdkman":
		return fmt.Sprintf("sdk install %s %s", name, v)
	default:
		return fmt.Sprintf("用 %s 安装 %s %s", manager, name, v)
	}
}

func runtimeManualReason(rt config.Runtime) string {
	if rt.Manager != nil {
		return fmt.Sprintf("未安装，且没有配方覆盖；该运行时由 %s 管理，建议用该管理器安装", *rt.Manager)
	}
	return "未安装，且没有配方覆盖"
}

// viable 报告该条目是否有可自动执行的层级（A4）。
func viable(it Item) bool {
	return it.Status == StatusPlanned && (it.Tier == TierWinget || it.Tier == TierLocal)
}

// compareItems 按 A2 的优先级比较两个条目：可装 > arch64 > HKCU > 版本高。
//
// ok 为 false 表示在版本这一层无法比较（版本号解析不了），此时**不做选择**（A3）。
func compareItems(a, b Item) (int, bool) {
	if va, vb := viable(a), viable(b); va != vb {
		if va {
			return 1, true
		}
		return -1, true
	}
	if a.ArchView != b.ArchView {
		if a.ArchView == "64" {
			return 1, true
		}
		if b.ArchView == "64" {
			return -1, true
		}
	}
	if a.Hive != b.Hive {
		if strings.EqualFold(a.Hive, "HKCU") {
			return 1, true
		}
		if strings.EqualFold(b.Hive, "HKCU") {
			return -1, true
		}
	}
	return vcompare.Compare(a.Expected, b.Expected)
}

// dedupeByRecipe 按 A1 处理同一配方的多个条目。
//
// multi_version=true 的产品（JDK / Python / Node / .NET SDK）每个版本独立保留；
// 其余产品只留一个代表，其余标 covered 并记录被谁覆盖（A5）。
func dedupeByRecipe(items []Item, recipes []config.Installer, prefer map[string]bool) {
	groups := map[string][]int{}
	var order []string
	for i, it := range items {
		if it.RecipeName == "" {
			continue
		}
		if _, ok := groups[it.RecipeName]; !ok {
			order = append(order, it.RecipeName)
		}
		groups[it.RecipeName] = append(groups[it.RecipeName], i)
	}

	for _, name := range order {
		idxs := groups[name]
		if len(idxs) < 2 {
			continue
		}
		if r := findRecipe(recipes, name); r != nil && r.MultiVersion {
			continue // 多版本共存是常态，不去重
		}

		rep, err := selectRepresentative(items, idxs, prefer)
		if err != nil {
			for _, i := range idxs {
				items[i].Status = StatusAmbiguous
				items[i].Tier = ""
				items[i].Command = ""
				items[i].Reason = err.Error()
			}
			continue
		}
		for _, i := range idxs {
			if i == rep {
				continue
			}
			items[i].Status = StatusCovered
			items[i].Tier = ""
			items[i].Command = ""
			items[i].Reason = fmt.Sprintf("被同配方的 %s（%s）覆盖，跳过（A1 去重）",
				items[rep].Name, orUnknown(items[rep].Expected))
		}
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "版本未知"
	}
	return s
}

// selectRepresentative 按 A2 的顺序选代表条目。
func selectRepresentative(items []Item, idxs []int, prefer map[string]bool) (int, error) {
	// ④ 决策优先于一切自动规则：用户明确说要哪一条，就按他说的来。
	var chosenByDecision []int
	for _, i := range idxs {
		if prefer[items[i].TargetID] {
			chosenByDecision = append(chosenByDecision, i)
		}
	}
	if len(chosenByDecision) > 1 {
		return -1, fmt.Errorf("决策把 %d 个同配方条目同时指定为代表，请只保留一条", len(chosenByDecision))
	}
	if len(chosenByDecision) == 1 {
		return chosenByDecision[0], nil
	}

	var pinned []int
	for _, i := range idxs {
		if items[i].Pinned {
			pinned = append(pinned, i)
		}
	}
	if len(pinned) > 1 {
		// A2.1：多个 pinned 说明用户自己标冲突了，报错不猜。
		return -1, fmt.Errorf("同配方有 %d 个条目被 x-pinned 标记，需人工去掉多余的标记", len(pinned))
	}
	if len(pinned) == 1 {
		return pinned[0], nil // 显式标记优先，即使它不可装也尊重用户选择
	}

	best := idxs[0]
	for _, i := range idxs[1:] {
		res, ok := compareItems(items[i], items[best])
		if !ok {
			// A3：版本号解析不了就不做选择。
			return -1, fmt.Errorf("同配方的多个条目版本号无法解析（%s / %s），需人工指定保留哪一个",
				orUnknown(items[i].Expected), orUnknown(items[best].Expected))
		}
		if res > 0 {
			best = i
		}
	}
	return best, nil
}

// orderItems 按 D11 排序：配方列表书写顺序为主序，depends_on 做拓扑排序。
// 检测到环直接报错，不产出可执行计划（C5/C10）。
func orderItems(items []Item, recipes []config.Installer) ([]Item, error) {
	recipePos := make(map[string]int, len(recipes))
	for i, r := range recipes {
		recipePos[strings.ToLower(r.Name)] = i
	}

	natural := make([]int, len(items))
	for i := range items {
		natural[i] = i
	}
	key := func(i int) int {
		if items[i].RecipeName == "" {
			return len(recipes) + i
		}
		if pos, ok := recipePos[strings.ToLower(items[i].RecipeName)]; ok {
			return pos
		}
		return len(recipes) + i
	}
	sort.SliceStable(natural, func(a, b int) bool { return key(natural[a]) < key(natural[b]) })

	idxByRecipe := make(map[string]int, len(items))
	for i, it := range items {
		if it.RecipeName != "" {
			idxByRecipe[strings.ToLower(it.RecipeName)] = i
		}
	}
	deps := make([][]int, len(items))
	indeg := make([]int, len(items))
	for i, it := range items {
		if it.RecipeName == "" {
			continue
		}
		r := findRecipe(recipes, it.RecipeName)
		if r == nil {
			continue
		}
		for _, d := range r.DependsOn {
			j, ok := idxByRecipe[strings.ToLower(d)]
			if !ok || j == i {
				continue
			}
			deps[j] = append(deps[j], i)
			indeg[i]++
		}
	}

	out := make([]Item, 0, len(items))
	done := make([]bool, len(items))
	for len(out) < len(items) {
		progressed := false
		for _, i := range natural {
			if done[i] || indeg[i] > 0 {
				continue
			}
			done[i] = true
			progressed = true
			out = append(out, items[i])
			for _, nxt := range deps[i] {
				indeg[nxt]--
			}
			break
		}
		if !progressed {
			return nil, fmt.Errorf("installers 依赖存在环，拒绝执行（C5）")
		}
	}

	// 记录被移动的原因：提前说明「被谁依赖」，推后说明「依赖谁」。
	naturalPos := make(map[int]int, len(natural))
	for pos, i := range natural {
		naturalPos[i] = pos
	}
	idxOf := make(map[string]int, len(items))
	for i, it := range items {
		if it.RecipeName != "" {
			idxOf[strings.ToLower(it.RecipeName)] = i
		}
	}
	for pos := range out {
		origIdx := -1
		for i := range items {
			if items[i].Name == out[pos].Name && items[i].Kind == out[pos].Kind {
				origIdx = i
				break
			}
		}
		if origIdx < 0 || naturalPos[origIdx] == pos {
			continue
		}
		reason := ""
		if pos < naturalPos[origIdx] {
			for j, other := range items {
				if j == origIdx || other.RecipeName == "" {
					continue
				}
				if r := findRecipe(recipes, other.RecipeName); r != nil {
					for _, d := range r.DependsOn {
						if out[pos].RecipeName != "" && strings.EqualFold(d, out[pos].RecipeName) {
							reason = "因被 " + other.Name + " 依赖而提前"
						}
					}
				}
			}
		} else if r := findRecipe(recipes, out[pos].RecipeName); r != nil && len(r.DependsOn) > 0 {
			for _, d := range r.DependsOn {
				if _, ok := idxOf[strings.ToLower(d)]; ok {
					reason = "依赖 " + d + " 需先执行"
					break
				}
			}
		}
		if reason == "" {
			reason = "因依赖关系调整顺序"
		}
		out[pos].MovedBy = reason
	}
	return out, nil
}

func findRecipe(recipes []config.Installer, name string) *config.Installer {
	for i := range recipes {
		if strings.EqualFold(recipes[i].Name, name) {
			return &recipes[i]
		}
	}
	return nil
}

func buildPathChanges(cfg *config.Config, opts Options) []PathChange {
	var out []PathChange
	existing := make(map[string]bool, len(opts.Target.PathDirs))
	for _, d := range opts.Target.PathDirs {
		existing[normPath(d)] = true
	}
	drives := make(map[string]bool, len(opts.Target.Drives))
	for _, d := range opts.Target.Drives {
		drives[strings.ToUpper(strings.TrimSuffix(d, ":"))] = true
	}

	add := func(section string, entries []config.PathEntry) {
		for _, e := range entries {
			raw := strings.TrimSpace(e.Value)
			// 展示值只代入 ${USER}/${HOST}，%VAR% 保持原样（B1：不把机器相关值固化）。
			display := opts.Expand(strings.Trim(raw, `"`))
			// A1-b：id 一律在**代入之后**的值上算。
			//
			// 若用未展开的 ${USER} 算 id，扫描侧（看到的是真实用户名）与 plan 侧
			// （看到的是展开后的值）会给出两个 id：排除导出引用前者、决策引用后者，
			// 而且是「静默失效」——难查得很。代价是带 ${USER} 的 PATH 决策只在本机
			// 有效（D17 本来就规定决策是本机侧的）。
			id := targetid.Path(section, display)
			if raw == "" {
				out = append(out, PathChange{TargetID: id, Section: section, Mark: MarkInvalid, Value: "(空条目)",
					Reason: "空条目，不写入"})
				continue
			}
			// 检查值额外展开 %VAR%，仅用于存在性与盘符判断，不写进任何输出。
			check := opts.ExpandVars(display)

			ch := PathChange{TargetID: id, Section: section, Mark: MarkAdd, Value: display}
			switch {
			case existing[normPath(check)]:
				ch.Mark = MarkSkip
				ch.Reason = "目标机已存在"
			case strings.Contains(raw, config.UserPlaceholder):
				ch.Mark = MarkRewrite
				ch.Reason = "用户名重写（D4-R1 唯一自动重写项）"
			case !drivePresent(check, drives):
				ch.Mark = MarkPending
				ch.Reason = "目标机无该盘符，需人工确认落点（D4-R1）"
			case !opts.Exists(check):
				ch.Mark = MarkInvalid
				ch.Reason = "目标机上该目录不存在"
			}
			out = append(out, ch)
		}
	}
	add("user", cfg.Path.User)
	add("machine", cfg.Path.Machine)
	return out
}

func drivePresent(path string, drives map[string]bool) bool {
	if len(path) >= 2 && path[1] == ':' {
		return drives[strings.ToUpper(string(path[0]))]
	}
	return true // UNC 或相对路径，不按盘符判断
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.Trim(p, `"`)
	p = strings.TrimRight(p, `\`)
	return strings.ToLower(p)
}

func summarize(p *Plan) {
	s := Summary{Total: len(p.Items)}
	for _, it := range p.Items {
		switch it.Status {
		case StatusSkipped:
			s.Skipped++
		case StatusManual:
			s.Manual++
		case StatusAmbiguous:
			s.Ambiguous++
		case StatusBlockedNeedsDecision:
			s.BlockedNeedsDecision++
		default:
			switch it.Tier {
			case TierWinget:
				s.Winget++
			case TierLocal:
				s.Local++
			}
		}
		if it.Decision != "" {
			s.ByDecision++
		}
	}
	for _, ch := range p.Path {
		if ch.Mark == MarkPending {
			s.Pending++
		}
		if ch.Decision != "" {
			s.ByDecision++
		}
		if ch.Status == StatusBlockedNeedsDecision {
			// 前端「N 项因未决策将被跳过」必须把 PATH 也算进去，
			// 只数 items 会少报。
			s.BlockedNeedsDecision++
		}
	}
	p.Summary = s
}
