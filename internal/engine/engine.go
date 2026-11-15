// Package engine 把内核函数装配成 API 层需要的 Deps。
//
// 这里是唯一同时接触「真实系统」与「HTTP 层」的地方；api 包本身保持纯 HTTP 语义，
// 测试时注入假 Deps（D-2/E3）。
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Elysia4319/envlift/internal/api"
	"github.com/Elysia4319/envlift/internal/apply"
	"github.com/Elysia4319/envlift/internal/config"
	"github.com/Elysia4319/envlift/internal/match"
	"github.com/Elysia4319/envlift/internal/pathenv"
	"github.com/Elysia4319/envlift/internal/pathmgmt"
	"github.com/Elysia4319/envlift/internal/plan"
	"github.com/Elysia4319/envlift/internal/probe"
	"github.com/Elysia4319/envlift/internal/recipes"
	"github.com/Elysia4319/envlift/internal/relocation"
	"github.com/Elysia4319/envlift/internal/report"
	"github.com/Elysia4319/envlift/internal/scan"
	"github.com/Elysia4319/envlift/internal/softscan"
	"github.com/Elysia4319/envlift/internal/sysinfo"
	"github.com/Elysia4319/envlift/internal/targetid"
	"github.com/Elysia4319/envlift/internal/task"
	"github.com/Elysia4319/envlift/internal/winreg"

	"gopkg.in/yaml.v3"
)

// Config 是引擎的运行参数。
type Config struct {
	ConfigPath   string
	InstallerDir string
	ReportsDir   string
	NetProbe     string
	ScanTimeout  time.Duration
	// DecisionsPath 是 envlift.decisions.yaml 的路径；留空则取配置同目录。
	DecisionsPath string
	// RelocationPath 是重装账本路径；留空则使用工作目录默认值。
	RelocationPath string
}

// Deps 装配出真实的 api.Deps。
func Deps(c Config) api.Deps {
	if c.ConfigPath == "" {
		c.ConfigPath = "envlift.yaml"
	}
	if c.InstallerDir == "" {
		c.InstallerDir = "installers"
	}
	if c.ReportsDir == "" {
		c.ReportsDir = "reports"
	}
	if c.NetProbe == "" {
		c.NetProbe = "cdn.winget.microsoft.com:443"
	}

	return api.Deps{
		Status:     func(ctx context.Context) (any, error) { return status(c) },
		Scan:       func(ctx context.Context, raw bool, rep task.Reporter) ([]byte, error) { return doScan(c, raw, rep) },
		Commit:     func(req api.CommitRequest) (api.CommitResult, error) { return commit(c, req) },
		LoadConfig: config.Load,
		BuildPlan: func(cfg *config.Config, decisions json.RawMessage) (*plan.Plan, error) {
			return buildPlan(c, cfg, decisions)
		},
		LoadDecisions: func() (plan.Decisions, error) { return LoadDecisionsFile(decisionsPath(c)) },
		SaveDecisions: func(ds plan.Decisions) error { return SaveDecisionsFile(decisionsPath(c), ds) },
		Apply: func(ctx context.Context, p *plan.Plan, conf api.Confirmations, rep task.Reporter) ([]byte, error) {
			return doApply(c, p, conf, rep)
		},
		Recheck:     func() (bool, string) { return probeNetwork(c) },
		Rollback:    func(journalID string) error { return rollback(c, journalID) },
		ListReports: func() ([]api.ReportInfo, error) { return listReports(c) },
		ReadReport:  func(name, format string) ([]byte, error) { return readReport(c, name, format) },
	}
}

func newScanner(c Config) *scan.Scanner {
	reg := winreg.New()
	return &scan.Scanner{
		Registry:     reg,
		Tree:         reg,
		WingetRunner: softscan.NewWingetRunner(),
		SysInfo:      sysinfo.New(reg),
		Prober:       probe.New(4 * time.Second),
		Redactor:     config.NewRedactor("", ""),
	}
}

func status(c Config) (any, error) {
	reg := winreg.New()
	info, err := sysinfo.New(reg).Collect()
	if err != nil {
		return nil, err
	}
	netOK, netNote := probeNetwork(c)

	files, _ := os.ReadDir(c.InstallerDir)
	installerCount := 0
	for _, f := range files {
		if !f.IsDir() {
			installerCount++
		}
	}

	return map[string]any{
		"os":              info.OSVersion,
		"build":           info.OSBuild,
		"product_name":    info.OSProductName,
		"arch":            info.Arch,
		"username":        info.Username,
		"hostname":        info.Hostname,
		"elevated":        info.Elevated,
		"winget_ok":       info.WingetAvailable,
		"winget_version":  info.WingetVersion,
		"network_ok":      netOK,
		"network_note":    netNote,
		"installer_dir":   absOrSelf(c.InstallerDir),
		"installer_files": installerCount,
		"config_path":     absOrSelf(c.ConfigPath),
		"recipes":         len(recipes.Builtin()),
	}, nil
}

type scanPayload struct {
	Config *config.Config `json:"config"`
	Stats  map[string]any `json:"stats"`
	// Targets 给每条 software 一个稳定标识（targetid）。
	//
	// ③ 的「排除导出」（A2）与 ④ 的决策回传都靠它引用条目——不能用显示名
	// （真实机器上会重复）也不能用数组下标（重算后会错位）。
	Targets []scanTarget `json:"targets"`
	// Recognition 是「配方覆盖情况」的预览辅助信息（M6：只用于审阅，不写进配置文件）。
	//
	// 统计口径落在 match.Summarize：CLI 的扫描预览与 GUI 的扫描页必须是同一份实现，
	// 否则「已识别 / 未识别」两个数字会在两个入口里给出不同答案。
	Recognition *match.Summary `json:"recognition,omitempty"`
	// RecognitionError 非空表示配方表本身有问题（例如 match_display 正则非法），
	// 此时覆盖情况不可用。**宁可报错也不能显示成 0 条**——0 会被读成「全都没配方」。
	RecognitionError string `json:"recognition_error,omitempty"`
}

// scanTarget 是软件条目与稳定标识的对照表。
type scanTarget struct {
	TargetID string `json:"target_id"`
	Index    int    `json:"index"`
	Name     string `json:"name"`
}

// scanTargets 按 config.software 顺序算出全部稳定标识。
func scanTargets(sw []config.Software) []scanTarget {
	out := make([]scanTarget, 0, len(sw))
	for i, s := range sw {
		out = append(out, scanTarget{TargetID: targetid.Software(s), Index: i, Name: s.Name})
	}
	return out
}

func doScan(c Config, raw bool, rep task.Reporter) ([]byte, error) {
	sc := newScanner(c)
	sc.Redactor.Raw = raw

	rep.Phase("读注册表与 PATH", 0, 3)
	res, err := sc.Run()
	if err != nil {
		return nil, err
	}
	rep.Log("info", fmt.Sprintf("PATH %d 条 · 运行时 %d 个",
		len(res.Config.Path.User)+len(res.Config.Path.Machine), len(res.Config.Runtimes)))
	rep.Phase("三源合并", 2, 3)

	stats := map[string]any{}
	if res.Soft != nil {
		stats["software_total"] = len(res.Config.Software)
		stats["raw"] = res.Soft.Raw
		stats["filtered"] = res.Soft.Filtered
		stats["by_source"] = res.Soft.BySource
		stats["winget_via"] = res.Soft.WingetVia
	}
	for _, w := range res.Warnings {
		rep.Log("warn", w)
	}
	payload := scanPayload{Config: res.Config, Stats: stats, Targets: scanTargets(res.Config.Software)}
	rec, recErr := match.Summarize(recipes.Builtin(), res.Config.Software)
	if recErr != nil {
		// 配方表本身有问题时，覆盖情况不可用——但绝不能让前端显示成「0 条已识别」，
		// 那会被读成「全都没配方」。宁可把错误原样送出去。
		rep.Log("warn", recErr.Error())
		payload.RecognitionError = recErr.Error()
	} else {
		payload.Recognition = &rec
		rep.Log("info", fmt.Sprintf("配方 %d 条 · 已识别 %d · 未识别 %d",
			rec.Recipes, rec.Recognized, rec.Unrecognized))
	}
	rep.Phase("完成", 3, 3)

	return json.Marshal(payload)
}

// commit 把扫描结果写成配置。
//
// A2 的扩展：Exclude 里的条目从 `software:` 挪到顶层 `x-excluded:` 区——不参与
// 还原（`x-` 前缀读端无条件忽略，D9），但信息可恢复，不是删掉。默认为空 = 全量导出。
func commit(c Config, req api.CommitRequest) (api.CommitResult, error) {
	var p scanPayload
	if err := json.Unmarshal(req.ScanResult, &p); err != nil {
		return api.CommitResult{}, fmt.Errorf("扫描结果无法解析: %w", err)
	}
	if p.Config == nil {
		return api.CommitResult{}, errors.New("扫描结果里没有配置")
	}
	out := req.Out
	if out == "" {
		out = c.ConfigPath
	}
	// D-4：写入路径白名单——只允许写配置目录下的文件。
	if !withinAllowed(out, c) {
		return api.CommitResult{}, api.ErrPathForbidden
	}

	total := len(p.Config.Software)
	kept, excluded, unknown := partitionExcluded(p.Config.Software, req.Exclude)
	if len(unknown) > 0 {
		// 静默忽略会让用户以为排除了、实际还写进去了。
		return api.CommitResult{}, &api.UnknownTargets{IDs: unknown}
	}
	if len(excluded) > 0 {
		p.Config.Software = kept
		if p.Config.Extra == nil {
			p.Config.Extra = map[string]interface{}{}
		}
		p.Config.Extra["x-excluded"] = excluded
	}

	if _, err := os.Stat(out); err == nil {
		switch req.OnExist {
		case "rename":
			base := strings.TrimSuffix(out, filepath.Ext(out))
			ext := filepath.Ext(out)
			for i := 2; i < 100; i++ {
				cand := fmt.Sprintf("%s-%d%s", base, i, ext)
				if _, err := os.Stat(cand); errors.Is(err, os.ErrNotExist) {
					out = cand
					break
				}
			}
		case "backup_overwrite":
			// config.Save 自己会先备份为 .bak 再原子替换。
		default:
			return api.CommitResult{}, fmt.Errorf("目标文件已存在且未指定处理方式: %s", out)
		}
	}
	if err := config.Save(out, p.Config); err != nil {
		return api.CommitResult{}, err
	}
	return api.CommitResult{Written: out, Total: total, Excluded: len(excluded)}, nil
}

// partitionExcluded 按 target_id 把软件条目切成「保留」与「排除」两段，顺序不变。
//
// unknown 返回请求里存在、扫描结果里不存在的 id：调用方必须报错。
func partitionExcluded(sw []config.Software, exclude []string) (kept, excluded []config.Software, unknown []string) {
	if len(exclude) == 0 {
		return sw, nil, nil
	}
	want := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		want[id] = true
	}
	seen := make(map[string]bool, len(exclude))
	kept = make([]config.Software, 0, len(sw))
	for _, s := range sw {
		id := targetid.Software(s)
		if want[id] {
			excluded = append(excluded, s)
			seen[id] = true
			continue
		}
		kept = append(kept, s)
	}
	for _, id := range exclude {
		if !seen[id] {
			unknown = append(unknown, id)
		}
	}
	return kept, excluded, unknown
}

func withinAllowed(path string, c Config) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	dir := filepath.Dir(abs)
	allowed := []string{filepath.Dir(absOrSelf(c.ConfigPath)), absOrSelf(c.ReportsDir), absOrSelf(".")}
	for _, a := range allowed {
		aa, err := filepath.Abs(a)
		if err != nil {
			continue
		}
		if strings.EqualFold(dir, aa) {
			return true
		}
	}
	return false
}

func buildPlan(c Config, cfg *config.Config, rawDecisions json.RawMessage) (*plan.Plan, error) {
	ds, err := parseDecisions(rawDecisions)
	if err != nil {
		return nil, err
	}
	sc := newScanner(c)
	env, err := sc.ProbeEnvironment()
	if err != nil {
		return nil, err
	}
	netOK, netNote := probeNetwork(c)

	target := plan.Target{
		OS: env.Info.OSVersion, Build: env.Info.OSBuild, Arch: env.Info.Arch,
		Username: env.Info.Username, Hostname: env.Info.Hostname, Elevated: env.Info.Elevated,
		WingetOK: env.Info.WingetAvailable, WingetVer: env.Info.WingetVersion,
		WingetForceOK: env.Info.WingetForceAvailable,
		NetworkOK:     netOK, NetworkNote: netNote,
		Runtimes: map[string]plan.RuntimeState{},
		PathDirs: env.PathDirs,
	}
	for _, d := range env.Info.Drives {
		target.Drives = append(target.Drives, d.Letter)
	}
	for _, r := range env.Runtimes {
		st := plan.RuntimeState{Present: r.Executable != nil}
		if r.Version != nil {
			st.Version = *r.Version
		}
		if r.Executable != nil {
			st.Path = *r.Executable
		}
		target.Runtimes[r.Name] = st
	}

	files, _ := os.ReadDir(c.InstallerDir)
	var names []string
	for _, f := range files {
		if !f.IsDir() {
			names = append(names, f.Name())
		}
	}
	absDir, _ := filepath.Abs(c.InstallerDir)
	red := config.NewRedactor(env.Info.Username, env.Info.Hostname)

	ledger, ledgerErr := relocation.Load(relocationPath(c))
	if ledgerErr != nil {
		ledger = relocation.New()
	}
	p, err := plan.Build(plan.Options{
		Config:         cfg,
		Target:         target,
		InstallerDir:   absDir,
		InstallerFiles: names,
		Recipes:        mergeRecipes(cfg),
		Now:            time.Now(),
		Exists:         func(p string) bool { _, err := os.Stat(p); return err == nil },
		Expand:         red.ExpandString,
		ExpandVars:     expandVars,
		RawConfig:      plan.DetectRaw(cfg),
		Decisions:      ds,
		Relocations:    ledger,
	})
	if err != nil {
		return nil, err
	}
	if ledgerErr != nil {
		p.Warnings = append(p.Warnings, "重装账本读取失败，按空账本继续: "+ledgerErr.Error())
	}
	return p, nil
}

func mergeRecipes(cfg *config.Config) []config.Installer {
	out := append([]config.Installer{}, recipes.Builtin()...)
	idx := map[string]int{}
	for i, r := range out {
		idx[strings.ToLower(r.Name)] = i
	}
	for _, r := range cfg.Installers {
		k := strings.ToLower(r.Name)
		if i, ok := idx[k]; ok {
			out[i] = r
			continue
		}
		idx[k] = len(out)
		out = append(out, r)
	}
	return out
}

func doApply(c Config, p *plan.Plan, conf api.Confirmations, rep task.Reporter) ([]byte, error) {
	dir := absOrSelf(c.ReportsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	jpath := filepath.Join(dir, ".envlift-journal-"+time.Now().Format("20060102-150405")+".json")
	j, err := apply.NewJournal(jpath, c.ConfigPath)
	if err != nil {
		return nil, err
	}

	ledger, ledgerErr := relocation.Load(relocationPath(c))
	if ledgerErr != nil {
		ledger = relocation.New()
	}
	res, err := apply.Run(apply.Options{
		Plan:           p,
		Journal:        j,
		Exec:           apply.NewExecutor(),
		Paths:          pathmgmt.New(),
		Locations:      apply.NewRegistryLocationReader(),
		Relocations:    ledger,
		RelocationPath: relocationPath(c),
		Out:            logWriter{rep},
		Elevated:       p.Target.Elevated,
		// D15：skip_admin 由执行器统一阻止提权项，不靠这里的回调兜底。
		SkipAdmin: conf.SkipAdmin(),
		// C-2：需提权项必须在 admin_confirmed 里逐项出现，缺失即视为未确认。
		Confirm: func(prompt string) bool {
			// 系统级 PATH 在 api 层已按「PATH[system] <值>」逐条校验过覆盖，
			// 这里放行只可能是 mode=all 且全量确认（skip_admin 已被上面拦掉）。
			if strings.Contains(prompt, "系统级 PATH") {
				return conf.Mode == api.ConfirmModeAll
			}
			for _, it := range p.Items {
				if it.NeedsAdmin && strings.Contains(prompt, it.Name) {
					return conf.HasAdmin(it.Name)
				}
			}
			return false
		},
		Timeout: 15 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	if ledgerErr != nil {
		res.Warnings = append(res.Warnings, "重装账本读取失败，按空账本继续: "+ledgerErr.Error())
		rep.Log("warn", "重装账本读取失败，按空账本继续: "+ledgerErr.Error())
	}

	if _, _, rerr := report.Write(dir, report.Input{
		Plan: p, Apply: res, Steps: j.Steps, JournalPath: j.Path(), ConfigPath: c.ConfigPath,
		ApplyMode: conf.Mode, Now: time.Now(),
		Redactor: config.NewRedactor(p.Target.Username, p.Target.Hostname),
	}); rerr != nil {
		rep.Log("warn", "写报告失败: "+rerr.Error())
	}

	out, _ := json.Marshal(res)
	if strings.Contains(res.Stopped, "重启") {
		return out, task.ErrAwaitReboot
	}
	return out, nil
}

// logWriter 把 apply 的输出转成任务日志。
type logWriter struct{ rep task.Reporter }

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSpace(string(p)), "\n") {
		if strings.TrimSpace(line) != "" {
			w.rep.Log("info", strings.TrimSpace(line))
		}
	}
	return len(p), nil
}

func rollback(c Config, journalID string) error {
	path := filepath.Join(absOrSelf(c.ReportsDir), journalID)
	j, err := apply.LoadJournal(path)
	if err != nil {
		return err
	}
	return apply.Rollback(j, pathmgmt.New(), os.Stdout)
}

func listReports(c Config) ([]api.ReportInfo, error) {
	dir := absOrSelf(c.ReportsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []api.ReportInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		var kind string
		switch {
		// 一份报告是 md + json 双份，列表只暴露 json 这一行；md 是同一份的另一种
		// 呈现（详情页的「看 md 原文」）。否则列表里每份报告会出现两次。
		case strings.HasPrefix(name, "restore-") && strings.HasSuffix(name, ".json"):
			kind = api.KindReport
		case strings.HasPrefix(name, ".envlift-journal-") && strings.HasSuffix(name, ".json"):
			// 回滚入口要靠它；顺带按路径类型区分，报告页可以只要 kind=report。
			kind = api.KindJournal
		default:
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ri := api.ReportInfo{
			Name:      name,
			Kind:      kind,
			Modified:  info.ModTime().Format(time.RFC3339),
			SizeBytes: info.Size(),
		}
		// 列表页直接用报告头部渲染摘要，不必再逐份拉详情。
		// 超过体积上限的只给 stat，别为了一个列表去读 5MB+。
		if kind == api.KindReport {
			if info.Size() > api.MaxReportBytes {
				ri.Truncated = true
			} else if data, rerr := os.ReadFile(filepath.Join(dir, name)); rerr == nil {
				if doc, perr := report.Parse(data); doc != nil {
					ri.ReportSchema = doc.ReportSchema
					if perr == nil {
						ri.Mode = doc.Mode
						ri.PlanHash = doc.PlanHash
						summary := doc.Summary
						env := doc.Env
						ri.Summary = &summary
						ri.Env = &env
					} else {
						ri.Unsupported = true
					}
				}
			}
		}
		out = append(out, ri)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified > out[j].Modified })
	return out, nil
}

func readReport(c Config, name, format string) ([]byte, error) {
	path := filepath.Join(absOrSelf(c.ReportsDir), name)
	if !strings.HasSuffix(strings.ToLower(path), "."+format) {
		return nil, fmt.Errorf("报告 %s 不是 %s 格式", name, format)
	}
	if info, err := os.Stat(path); err == nil && info.Size() > api.MaxReportBytes {
		// 明确报错，而不是让前端等一个几 MB 的响应然后超时。
		return nil, api.ErrReportTooLarge
	}
	return os.ReadFile(path)
}

func probeNetwork(c Config) (bool, string) {
	conn, err := net.DialTimeout("tcp", c.NetProbe, 3*time.Second)
	if err != nil {
		return false, err.Error()
	}
	_ = conn.Close()
	return true, ""
}

func absOrSelf(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func relocationPath(c Config) string {
	if c.RelocationPath != "" {
		return c.RelocationPath
	}
	return relocation.DefaultPath
}

func expandVars(s string) string { return pathenv.ExpandVars(s) }

// decisionsPath 返回决策文件路径：默认与配置同目录的 envlift.decisions.yaml。
//
// 决策是「本机侧的选择」，不是环境快照的一部分（D17），所以不写进 envlift.yaml，
// 也进 .gitignore。
func decisionsPath(c Config) string {
	if c.DecisionsPath != "" {
		return c.DecisionsPath
	}
	dir := filepath.Dir(absOrSelf(c.ConfigPath))
	return filepath.Join(dir, "envlift.decisions.yaml")
}

// parseDecisions 把请求里的 decisions 数组解析成 plan.Decisions。
//
// 结构错误在这里就要拒绝（严格解码，未知字段报错）——放过去只会在执行时才炸。
func parseDecisions(raw json.RawMessage) (plan.Decisions, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var ds plan.Decisions
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ds); err != nil {
		return nil, &plan.DecisionErrors{Issues: []plan.DecisionIssue{{
			Index:  0, // 0 = 整体结构问题，不对应某一条
			Code:   plan.CodeInvalidDecisions,
			Reason: "decisions 结构不合法: " + err.Error(),
		}}}
	}
	return ds, nil
}

// LoadDecisionsFile 读取 envlift.decisions.yaml。文件不存在等同于「没有决策」。
//
// 导出给 CLI 用：GUI 与 CLI 必须读同一份文件、按同一套规则解析（C4）。
func LoadDecisionsFile(path string) (plan.Decisions, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var f plan.DecisionsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("解析决策文件 %s 失败: %w", path, err)
	}
	if f.App != "" && f.App != config.AppName {
		return nil, fmt.Errorf("决策文件 %s 的 app 字段应为 %q，实际为 %q", path, config.AppName, f.App)
	}
	if f.Schema > config.CurrentSchema {
		return nil, fmt.Errorf("决策文件 %s 的 schema=%d 高于本程序支持的 %d，请升级 envlift",
			path, f.Schema, config.CurrentSchema)
	}
	return f.Decisions, nil
}

// SaveDecisionsFile 原子写入决策文件（先写 .tmp 再改名）。
func SaveDecisionsFile(path string, ds plan.Decisions) error {
	f := plan.DecisionsFile{App: config.AppName, Schema: config.CurrentSchema, Decisions: ds}
	if f.Decisions == nil {
		f.Decisions = plan.Decisions{}
	}
	var buf bytes.Buffer
	buf.WriteString("# envlift 决策文件（本机选择，不是环境快照；可手工编辑）\n")
	buf.WriteString("# 校验以 target_id 为准；name / version / value 仅供人读。\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return fmt.Errorf("序列化决策失败: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("序列化决策失败: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("写入决策临时文件失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换决策文件失败: %w", err)
	}
	return nil
}
