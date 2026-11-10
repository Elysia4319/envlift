package apply

import (
	"regexp"
	"strings"
)

// versionToken 匹配输出里"像版本号"的片段：至少两段数字，允许三段与常见后缀。
//
// 刻意保守：宁可解析不出来（留空 + 标 unparsed），也不要把 "2024"、"v1" 这种
// 当版本回填——错的版本比没有版本更坏（报告会被拿来跨机器对比）。
//
// 不敢用前导 \b：`v22.22.2` 里 v 与 2 都是词字符，\b 不成立会把「22.2」
// 当成版本（真踩过）。这里靠「必须含点」+ 去掉尾部句点来保证形状。
var versionToken = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?(?:[-+][0-9A-Za-z.]+)?`)

// ParseVersion 从 verify 命令的输出里抽出版本号。
//
// verify 跑的就是 `foo --version` 这类命令，输出里通常就有版本（Git 的
// "git version 2.47.1.windows.1"、node 的 "v22.22.2" 都覆盖）。ok=false 表示
// 输出里找不到像版本的东西——调用方应留空并标记，不要编。
func ParseVersion(out string) (version string, ok bool) {
	line := strings.TrimSpace(out)
	if line == "" {
		return "", false
	}
	if m := versionToken.FindString(line); m != "" {
		m = strings.TrimRight(m, ".")
		if strings.Count(m, ".") >= 1 && m != "" {
			return m, true
		}
	}
	return "", false
}
