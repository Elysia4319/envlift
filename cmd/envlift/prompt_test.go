package main

import (
	"io"
	"strings"
	"testing"
)

// 管道输入的真实形态：第一行 APPLY，后面跟着逐项确认。
// 共享 reader 时每一行都必须被消费；每次新建 bufio.Reader 会预读并丢弃后续行。
func TestPrompterSharesReaderAcrossApplyAndItemConfirmations(t *testing.T) {
	pr := newPrompter(strings.NewReader("APPLY\ny\ny\n"), io.Discard)

	if !pr.applyConfirmation() {
		t.Fatal("APPLY 应被接受")
	}
	if !pr.confirm("条目 A") {
		t.Fatal("第 1 个提权确认应为 true（共享 reader）")
	}
	if !pr.confirm("条目 B") {
		t.Fatal("第 2 个提权确认应为 true（共享 reader）")
	}
}

func TestPrompterAppliesCaseSensitiveApplyAndPerItemAnswers(t *testing.T) {
	pr := newPrompter(strings.NewReader("apply\nn\nyes\n"), io.Discard)

	if pr.applyConfirmation() {
		t.Fatal("小写 apply 应被拒绝（区分大小写）")
	}
	if pr.confirm("条目 A") {
		t.Fatal("n 应为 false")
	}
	if !pr.confirm("条目 B") {
		t.Fatal("yes 应为 true")
	}
}

func TestPrompterTreatsEOFLineAsInput(t *testing.T) {
	pr := newPrompter(strings.NewReader("APPLY"), io.Discard)
	if !pr.applyConfirmation() {
		t.Fatal("末尾没有换行符的 APPLY 也应被接受")
	}

	empty := newPrompter(strings.NewReader(""), io.Discard)
	if empty.confirm("条目 A") {
		t.Fatal("没有输入时应按默认 N 处理")
	}
}

// PowerShell 把字符串管道给原生 exe 时会带 UTF-8 BOM。
func TestPrompterStripsUTF8BOMFromFirstLine(t *testing.T) {
	pr := newPrompter(strings.NewReader("\ufeffAPPLY\r\ny\r\n"), io.Discard)
	if !pr.applyConfirmation() {
		t.Fatal("带 BOM 的 APPLY 应被接受（PowerShell 管道）")
	}
	if !pr.confirm("条目 A") {
		t.Fatal("BOM 之后的行也应正常读取")
	}
}
