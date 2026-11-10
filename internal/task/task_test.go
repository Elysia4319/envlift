package task

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// waitTerminal 等到任务进入终态，避免测试里睡固定时长。
func waitTerminal(t *testing.T, m *Manager, id string) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, ok := m.Get(id, 0)
		if ok && s.State.Terminal() {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("任务 %s 未在超时内进入终态", id)
	return Snapshot{}
}

func TestStateMachineSuccess(t *testing.T) {
	m := NewManager(20, 100)
	id, err := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
		r.Phase("读注册表", 1, 3)
		r.Log("info", "第一步")
		r.Phase("winget list", 2, 3)
		return []byte(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	s := waitTerminal(t, m, id)
	if s.State != Succeeded {
		t.Errorf("状态 = %q，期望 succeeded", s.State)
	}
	if string(s.Result) != `{"ok":true}` {
		t.Errorf("结果未保存: %s", s.Result)
	}
	if len(s.Logs) != 1 || s.Logs[0].Seq != 1 {
		t.Errorf("日志不对: %#v", s.Logs)
	}
}

func TestFailureCaptured(t *testing.T) {
	m := NewManager(20, 100)
	id, _ := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
		return nil, errors.New("注册表读不到")
	})
	s := waitTerminal(t, m, id)
	if s.State != Failed {
		t.Errorf("状态 = %q，期望 failed", s.State)
	}
	if !strings.Contains(s.Error, "注册表读不到") {
		t.Errorf("错误信息未保留: %q", s.Error)
	}
}

// awaiting_reboot 是独立终态，不是 failed（B1/P2）。
func TestAwaitingRebootIsTerminalNotFailed(t *testing.T) {
	m := NewManager(20, 100)
	id, _ := m.Start("apply", func(ctx context.Context, r Reporter) ([]byte, error) {
		return nil, ErrAwaitReboot
	})
	s := waitTerminal(t, m, id)
	if s.State != AwaitingReboot {
		t.Errorf("状态 = %q，期望 awaiting_reboot", s.State)
	}
	if s.Error != "" {
		t.Errorf("awaiting_reboot 不应带错误信息: %q", s.Error)
	}
	if !s.State.Terminal() {
		t.Error("awaiting_reboot 应是终态")
	}
}

func TestSingleTaskLock(t *testing.T) {
	m := NewManager(20, 100)
	block := make(chan struct{})
	first, err := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
		<-block
		return nil, nil
	})
	if err != nil {
		t.Fatalf("首个任务应能启动: %v", err)
	}

	id, err := m.Start("apply", func(ctx context.Context, r Reporter) ([]byte, error) { return nil, nil })
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("第二个任务应返回 ErrBusy，实际 %v", err)
	}
	if id != first {
		t.Errorf("应回传当前任务 id，实际 %q 期望 %q", id, first)
	}
	if busyID, _, busy := m.Busy(); !busy || busyID != first {
		t.Errorf("Busy 应报告 %q", first)
	}

	close(block)
	waitTerminal(t, m, first)

	if _, _, busy := m.Busy(); busy {
		t.Error("任务结束后不应再忙")
	}
	if _, err := m.Start("apply", func(ctx context.Context, r Reporter) ([]byte, error) { return nil, nil }); err != nil {
		t.Errorf("上一个任务结束后应能启动新任务: %v", err)
	}
}

func TestSeqIncremental(t *testing.T) {
	m := NewManager(20, 100)
	id, _ := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
		for i := 0; i < 3; i++ {
			r.Log("info", "line")
		}
		return nil, nil
	})
	s := waitTerminal(t, m, id)
	if len(s.Logs) != 3 {
		t.Fatalf("应有 3 条日志，实际 %d", len(s.Logs))
	}
	if s.NextSeq != 4 {
		t.Errorf("next_seq = %d，期望 4", s.NextSeq)
	}

	// since=0 返回全部
	if got, _ := m.Get(id, 0); len(got.Logs) != 3 {
		t.Errorf("since=0 应返回全部，实际 %d", len(got.Logs))
	}
	// since=2 只返回 seq 3
	got, _ := m.Get(id, 2)
	if len(got.Logs) != 1 || got.Logs[0].Seq != 3 {
		t.Errorf("since=2 应只返回 seq=3，实际 %#v", got.Logs)
	}
	// since=next_seq-1 无新增
	if got, _ := m.Get(id, s.NextSeq-1); len(got.Logs) != 0 {
		t.Errorf("since 已到末尾应无新增，实际 %d", len(got.Logs))
	}
	// since 超出上限也无新增
	if got, _ := m.Get(id, 999); len(got.Logs) != 0 {
		t.Errorf("since 超出范围应无新增，实际 %d", len(got.Logs))
	}
}

func TestLogRingBuffer(t *testing.T) {
	m := NewManager(20, 5)
	id, _ := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
		for i := 0; i < 8; i++ {
			r.Log("info", "line")
		}
		return nil, nil
	})
	s := waitTerminal(t, m, id)
	if len(s.Logs) != 5 {
		t.Fatalf("日志应被截到 5 条，实际 %d", len(s.Logs))
	}
	// 保留的是最新的 5 条，seq 应为 4..8
	if s.Logs[0].Seq != 4 || s.Logs[4].Seq != 8 {
		t.Errorf("应保留最新 5 条，实际 seq %d..%d", s.Logs[0].Seq, s.Logs[4].Seq)
	}
}

func TestCancel(t *testing.T) {
	m := NewManager(20, 100)
	started := make(chan struct{})
	id, _ := m.Start("apply", func(ctx context.Context, r Reporter) ([]byte, error) {
		close(started)
		<-ctx.Done() // 模拟在阶段之间检查取消标志
		return nil, ctx.Err()
	})
	<-started
	if !m.Cancel(id) {
		t.Fatal("Cancel 应返回 true")
	}
	s := waitTerminal(t, m, id)
	if s.State != Cancelled {
		t.Errorf("状态 = %q，期望 cancelled", s.State)
	}
	// 已结束的任务再取消应返回 false
	if m.Cancel(id) {
		t.Error("终态任务不应再可取消")
	}
}

func TestCancelUnknownID(t *testing.T) {
	m := NewManager(20, 100)
	if m.Cancel("nope") {
		t.Error("未知任务不应取消成功")
	}
}

func TestGetUnknown(t *testing.T) {
	m := NewManager(20, 100)
	if _, ok := m.Get("nope", 0); ok {
		t.Error("未知任务应返回 false")
	}
}

func TestTrimKeepsRecent(t *testing.T) {
	m := NewManager(3, 100)
	var ids []string
	for i := 0; i < 5; i++ {
		id, err := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) { return nil, nil })
		if err != nil {
			t.Fatalf("第 %d 个任务启动失败: %v", i, err)
		}
		ids = append(ids, id)
		waitTerminal(t, m, id)
	}
	if _, ok := m.Get(ids[0], 0); ok {
		t.Error("最旧的任务应被淘汰")
	}
	if _, ok := m.Get(ids[4], 0); !ok {
		t.Error("最新任务应保留")
	}
}

func TestPhaseProgress(t *testing.T) {
	m := NewManager(20, 100)
	block := make(chan struct{})
	id, _ := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
		r.Phase("winget list", 2, 4)
		<-block
		return nil, nil
	})
	// 等 phase 写入
	var s Snapshot
	for i := 0; i < 100; i++ {
		s, _ = m.Get(id, 0)
		if s.Phase == "winget list" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if s.Phase != "winget list" || s.Done != 2 || s.Total != 4 {
		t.Errorf("进度上报不对: %+v", s)
	}
	close(block)
	waitTerminal(t, m, id)
}

func TestConcurrentStartRace(t *testing.T) {
	m := NewManager(20, 100)
	block := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	okCount := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Start("scan", func(ctx context.Context, r Reporter) ([]byte, error) {
				<-block
				return nil, nil
			}); err == nil {
				mu.Lock()
				okCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if okCount != 1 {
		t.Errorf("并发启动应只有一个成功，实际 %d", okCount)
	}
	close(block)
}
