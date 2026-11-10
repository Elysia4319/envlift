// Package vcompare 做语义化版本比较。
//
// 为什么不能直接比字符串：字符串比较下 "9" > "17"、"1.9" > "1.10" 都是错的。
// 去重时要挑版本最高的代表，比错就等于挑错。
package vcompare

import (
	"strconv"
	"strings"
)

type parsed struct {
	nums []int
	pre  string // 预发布后缀，空表示正式版
}

// Parse 解析版本号。ok 为 false 表示无法解析——调用方**不得**在这种情况下猜大小。
//
// 容忍：前后空白、v/V 前缀、-rc/-beta 这类预发布后缀、+build 元数据。
func Parse(s string) (parsed, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return parsed{}, false
	}
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}

	var pre string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = strings.ToLower(strings.TrimSpace(s[i+1:]))
		s = s[:i]
	}
	if s == "" {
		return parsed{}, false
	}

	parts := strings.Split(s, ".")
	nums := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return parsed{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return parsed{}, false
		}
		nums = append(nums, n)
	}
	return parsed{nums: nums, pre: pre}, true
}

// Compare 比较两个版本号，返回 -1 / 0 / 1。
// ok 为 false 表示至少一个无法解析，此时返回值无意义。
func Compare(a, b string) (int, bool) {
	pa, ok1 := Parse(a)
	pb, ok2 := Parse(b)
	if !ok1 || !ok2 {
		return 0, false
	}

	n := len(pa.nums)
	if len(pb.nums) > n {
		n = len(pb.nums)
	}
	for i := 0; i < n; i++ {
		x, y := 0, 0
		if i < len(pa.nums) {
			x = pa.nums[i]
		}
		if i < len(pb.nums) {
			y = pb.nums[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}

	// 数字部分相同：有预发布后缀的更低（1.2.3-rc1 < 1.2.3）。
	switch {
	case pa.pre == pb.pre:
		return 0, true
	case pa.pre == "":
		return 1, true
	case pb.pre == "":
		return -1, true
	default:
		return strings.Compare(pa.pre, pb.pre), true
	}
}

// Valid 报告版本号是否可解析。
func Valid(s string) bool {
	_, ok := Parse(s)
	return ok
}
