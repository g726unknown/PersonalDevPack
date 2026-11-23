package observer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// stubExec 是函数式的 Execer 假实现：记录每次调用（"name arg1 arg2"），
// 并按 run 应答。一个 run 函数比 map[命令]输出 灵活——
// 能模拟“第一次成功第二次失败”这类时序。
type stubExec struct {
	calls []string
	run   func(name string, args []string) ([]byte, []byte, error)
}

func (s *stubExec) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	s.calls = append(s.calls, name+" "+strings.Join(args, " "))
	return s.run(name, args)
}

// pacmanStub 按给定的 -Q / -Qm 输出应答，覆盖固定输出的用例；
// 需要时序或特殊错误的用例自己写 run。
func pacmanStub(qOut, qErr string, qErrExit error, mOut string) *stubExec {
	return &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		if name != "pacman" {
			return nil, nil, fmt.Errorf("unexpected command %q", name)
		}
		switch args[0] {
		case "-Q":
			return []byte(qOut), []byte(qErr), qErrExit
		case "-Qm":
			return []byte(mOut), nil, nil
		default:
			return nil, nil, fmt.Errorf("unexpected args %v", args)
		}
	}}
}

// assertStatuses 逐位比较；PkgStatus 全字段可比较，直接用 !=。
func assertStatuses(t *testing.T, got, want []PkgStatus) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(statuses) = %d, want %d (got %+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statuses[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPacmanQueryAllInstalled(t *testing.T) {
	stub := pacmanStub("neovim 0.11.2\nhtop 3.4.0\n", "", nil, "")
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim", "htop"})
	if err != nil {
		t.Fatalf("PacmanQuery() error = %v, want nil", err)
	}
	assertStatuses(t, got, []PkgStatus{
		{Name: "neovim", Installed: true, Version: "0.11.2"},
		{Name: "htop", Installed: true, Version: "3.4.0"},
	})
	if len(stub.calls) != 2 {
		t.Errorf("exec calls = %d, want 2 (-Q 与 -Qm 各一次): %v", len(stub.calls), stub.calls)
	}
}

func TestPacmanQueryPartialNotFoundIsNotAnError(t *testing.T) {
	// 一个装了、一个没装：pacman -Q 退出码 1，stderr 说明缺哪个。
	stub := pacmanStub(
		"neovim 0.11.2\n",
		"error: package 'htop' was not found\n",
		errors.New("exit status 1"),
		"",
	)
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim", "htop"})
	if err != nil {
		t.Fatalf("PacmanQuery() error = %v, want nil", err)
	}
	assertStatuses(t, got, []PkgStatus{
		{Name: "neovim", Installed: true, Version: "0.11.2"},
		{Name: "htop", Installed: false, Version: ""},
	})
}

func TestPacmanQueryForeignFlag(t *testing.T) {
	// -Qm 是全量名单：可能包含本次没查询的 foreign 包，
	// 只有请求过的名字才出现在结果里。
	stub := pacmanStub(
		"neovim 0.11.2\nvisual-studio-code-bin 1.90.0\n",
		"", nil,
		"unrelated-aur 2.1\nvisual-studio-code-bin 1.90.0\n",
	)
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim", "visual-studio-code-bin"})
	if err != nil {
		t.Fatalf("PacmanQuery() error = %v, want nil", err)
	}
	assertStatuses(t, got, []PkgStatus{
		{Name: "neovim", Installed: true, Version: "0.11.2", Foreign: false},
		{Name: "visual-studio-code-bin", Installed: true, Version: "1.90.0", Foreign: true},
	})
}

func TestPacmanQueryPacmanMissing(t *testing.T) {
	stub := &stubExec{run: func(name string, _ []string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("exec %s: %w", name, exec.ErrNotFound)
	}}
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim"})
	if err == nil {
		t.Fatal("PacmanQuery() error = nil, want error")
	}
	if got != nil {
		t.Errorf("PacmanQuery() = %+v, want nil", got)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("errors.Is(err, exec.ErrNotFound) = false, err = %v", err)
	}
}

func TestPacmanQueryRealErrorIsNotSwallowed(t *testing.T) {
	// 退出码同样是 1，但 stderr 是数据库错误：必须当真实错误上报，
	// 不能被误判成“部分没装”。
	stub := pacmanStub(
		"",
		"error: database 'sync' is inconsistent\n",
		errors.New("exit status 1"),
		"",
	)
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim"})
	if err == nil {
		t.Fatal("PacmanQuery() error = nil, want error")
	}
	if got != nil {
		t.Errorf("PacmanQuery() = %+v, want nil", got)
	}
}

func TestPacmanQueryNoNamesRunsNothing(t *testing.T) {
	stub := pacmanStub("", "", nil, "") // 不应被调用
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), nil)
	if err != nil {
		t.Fatalf("PacmanQuery(nil) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len(statuses) = %d, want 0", len(got))
	}
	if len(stub.calls) != 0 {
		t.Errorf("exec calls = %d, want 0: %v", len(stub.calls), stub.calls)
	}
}

func TestPacmanQueryEmptyNameRejectedBeforeAnyCommand(t *testing.T) {
	stub := pacmanStub("", "", nil, "") // 不应被调用
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim", ""})
	if err == nil {
		t.Fatal("PacmanQuery() error = nil, want error")
	}
	if got != nil {
		t.Errorf("PacmanQuery() = %+v, want nil", got)
	}
	if len(stub.calls) != 0 {
		t.Errorf("exec calls = %d, want 0: %v", len(stub.calls), stub.calls)
	}
}

func TestPacmanQueryIgnoresMalformedLines(t *testing.T) {
	// 空行、单字段、五字段的行都忽略；正常行不受影响。
	stub := pacmanStub(
		"neovim 0.11.2\n\nweird line with five fields\ngarbage\nhtop 3.4.0\n",
		"", nil,
		"single\n",
	)
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim", "htop"})
	if err != nil {
		t.Fatalf("PacmanQuery() error = %v, want nil", err)
	}
	assertStatuses(t, got, []PkgStatus{
		{Name: "neovim", Installed: true, Version: "0.11.2"},
		{Name: "htop", Installed: true, Version: "3.4.0"},
	})
}

func TestPacmanQueryDedupsKeepingFirstOrder(t *testing.T) {
	stub := pacmanStub("b 2.0\na 1.0\n", "", nil, "")
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"b", "a", "b"})
	if err != nil {
		t.Fatalf("PacmanQuery() error = %v, want nil", err)
	}
	assertStatuses(t, got, []PkgStatus{
		{Name: "b", Installed: true, Version: "2.0"},
		{Name: "a", Installed: true, Version: "1.0"},
	})
	// 去重后的名字按首次出现顺序传给 pacman。
	if want := "pacman -Q b a"; len(stub.calls) == 0 || stub.calls[0] != want {
		t.Errorf("first call = %q, want %q", stub.calls, want)
	}
}

func TestPacmanQueryNoForeignPackagesIsNotAnError(t *testing.T) {
	// 机器上没有任何 foreign 包时 pacman -Qm 以空 stderr 退出 1：
	// 查询无结果，不是故障，Foreign 全为 false。
	stub := &stubExec{run: func(_ string, args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "-Q":
			return []byte("neovim 0.11.2\n"), nil, nil
		case "-Qm":
			return nil, nil, errors.New("exit status 1")
		default:
			return nil, nil, fmt.Errorf("unexpected args %v", args)
		}
	}}
	o := New(stub)

	got, err := o.PacmanQuery(context.Background(), []string{"neovim"})
	if err != nil {
		t.Fatalf("PacmanQuery() error = %v, want nil", err)
	}
	assertStatuses(t, got, []PkgStatus{
		{Name: "neovim", Installed: true, Version: "0.11.2", Foreign: false},
	})
}

func TestPacmanQueryForeignDBErrorIsAnError(t *testing.T) {
	// -Qm 非零退出但 stderr 非空：宁可误报也不静默当“没有 foreign”。
	stub := &stubExec{run: func(_ string, args []string) ([]byte, []byte, error) {
		switch args[0] {
		case "-Q":
			return []byte("neovim 0.11.2\n"), nil, nil
		case "-Qm":
			return nil, []byte("error: database 'local' is inconsistent\n"), errors.New("exit status 1")
		default:
			return nil, nil, fmt.Errorf("unexpected args %v", args)
		}
	}}
	o := New(stub)

	if _, err := o.PacmanQuery(context.Background(), []string{"neovim"}); err == nil {
		t.Fatal("PacmanQuery() error = nil, want error")
	}
}
