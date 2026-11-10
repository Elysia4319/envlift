// Package match 把软件条目的原始 DisplayName 归一到可比较的形式，并按
// 四级锚点找到对应配方（M2/M3）。
package match

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	reTrademark = regexp.MustCompile(`[®™©]`)

	// 括号里的架构/安装范围后缀：(User)、(x64)、(64-bit x64) 等，可能含多个词。
	reBracketSuffix = regexp.MustCompile(`(?i)\s*[\(\[]\s*(?:(?:user|machine|x64|x86|arm64|64-bit|32-bit|64 bit|32 bit)\s*)+[\)\]]`)

	// 裸的架构后缀：Foo x64 / Foo 64-bit / Foo 7-x64（连字符连接）。
	reBareArch = regexp.MustCompile(`(?i)(?:\s+|-)(?:x64|x86|arm64|64-bit|32-bit|64 bit|32 bit)$`)

	// 尾部版本号：2.47.1 / v1.2 / 2026（单个数字也算，年份会被它先吃掉）。
	reTrailingVersion = regexp.MustCompile(`(?i)\s+v?\d+(?:\.\d+)*$`)

	// 尾部 "version" 字样：Git version 2.47.1 去掉版本号后剩 "git version"。
	reTrailingVersionWord = regexp.MustCompile(`(?i)\s+version$`)

	// 尾部年份（版本号规则已覆盖大部分，这里兜底）。
	reTrailingYear = regexp.MustCompile(`(?i)\s+20\d{2}$`)

	// 尾部残留的标点与连接符。
	reTrailingPunct = regexp.MustCompile(`[\s\-–—,;:]+$`)

	reSpaces = regexp.MustCompile(`\s+`)
)

// Normalize 把软件名归一到可比较形式（M3）。
//
// 依次做：Unicode NFC → trim → 小写折叠 → 去 ®™© → 去括号/裸架构后缀 →
// 去尾部版本号 → 去尾部 "version" → 去尾部年份 → 去尾部标点 → 折叠空白。
// 后缀剥离循环到稳定，因为 "Foo 2024 v1.2" 这类要剥两轮。
//
// 纯函数，无 IO，便于单测。
func Normalize(s string) string {
	s = norm.NFC.String(s)
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	s = reTrademark.ReplaceAllString(s, "")

	for i := 0; i < 4; i++ {
		prev := s
		s = reBracketSuffix.ReplaceAllString(s, "")
		s = reBareArch.ReplaceAllString(s, "")
		s = reTrailingVersion.ReplaceAllString(s, "")
		s = reTrailingVersionWord.ReplaceAllString(s, "")
		s = reTrailingYear.ReplaceAllString(s, "")
		s = strings.TrimSpace(s)
		s = reTrailingPunct.ReplaceAllString(s, "")
		if s == prev {
			break
		}
	}

	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
