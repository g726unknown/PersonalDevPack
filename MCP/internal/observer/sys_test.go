package observer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 本文件复用 pacman_test.go 里的 stubExec（同一个测试包）。

func TestParseOSRelease(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "os-release"))
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	id, pretty, versionID := parseOSRelease(raw)
	if id != "arch" {
		t.Errorf("ID = %q, want %q", id, "arch")
	}
	if pretty != "Arch Linux" {
		t.Errorf("PRETTY_NAME = %q, want %q", pretty, "Arch Linux")
	}
	// 样本故意不含 VERSION_ID（滚动发行版）：空是事实，不是错误。
	if versionID != "" {
		t.Errorf("VERSION_ID = %q, want empty", versionID)
	}
}

func TestReadOSReleaseMissingFile(t *testing.T) {
	if _, err := readOSRelease(filepath.Join(t.TempDir(), "no-such-file")); err == nil {
		t.Fatal("readOSRelease() error = nil, want error")
	}
}

func TestPacmanVersionFindsBannerBehindLogo(t *testing.T) {
	stub := &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		if name != "pacman" || len(args) != 1 || args[0] != "--version" {
			return nil, nil, fmt.Errorf("unexpected %s %v", name, args)
		}
		// 真机形状：首行带 ASCII logo，版本横幅不独占一行。
		return []byte(` .--.                  Pacman v6.1.0 - libalpm v15.0.0
/ _.-' .-.  .-.  .-.   Copyright (C) 2006-2026 Pacman Development Team
\  '-. '-'  '-'  '-'
 '--'
`), nil, nil
	}}

	got, err := New(stub).PacmanVersion(context.Background())
	if err != nil {
		t.Fatalf("PacmanVersion() error = %v, want nil", err)
	}
	if !got.Available {
		t.Error("Available = false, want true")
	}
	if got.Version != "Pacman v6.1.0 - libalpm v15.0.0" {
		t.Errorf("Version = %q, want %q", got.Version, "Pacman v6.1.0 - libalpm v15.0.0")
	}
}

func TestPacmanVersionNotInstalledIsNotAnError(t *testing.T) {
	stub := &stubExec{run: func(name string, _ []string) ([]byte, []byte, error) {
		return nil, nil, fmt.Errorf("exec %s: %w", name, exec.ErrNotFound)
	}}

	got, err := New(stub).PacmanVersion(context.Background())
	if err != nil {
		t.Fatalf("PacmanVersion() error = %v, want nil", err)
	}
	if got.Available {
		t.Error("Available = true, want false")
	}
	if got.Version != "" {
		t.Errorf("Version = %q, want empty", got.Version)
	}
}

func TestPacmanVersionRealErrorIsAnError(t *testing.T) {
	stub := &stubExec{run: func(_ string, _ []string) ([]byte, []byte, error) {
		return nil, []byte("error: something broke\n"), errors.New("exit status 1")
	}}

	if _, err := New(stub).PacmanVersion(context.Background()); err == nil {
		t.Fatal("PacmanVersion() error = nil, want error")
	}
}

func TestPacmanVersionEmptyOutputIsAnError(t *testing.T) {
	stub := &stubExec{run: func(_ string, _ []string) ([]byte, []byte, error) {
		return nil, nil, nil
	}}

	if _, err := New(stub).PacmanVersion(context.Background()); err == nil {
		t.Fatal("PacmanVersion() error = nil, want error for empty output")
	}
}

func TestPacmanVersionNoBannerIsAnError(t *testing.T) {
	// 有输出但没有版本横幅：不允许静默报成“可用但版本未知”。
	stub := &stubExec{run: func(_ string, _ []string) ([]byte, []byte, error) {
		return []byte("unexpected output without banner\n"), nil, nil
	}}

	if _, err := New(stub).PacmanVersion(context.Background()); err == nil {
		t.Fatal("PacmanVersion() error = nil, want error for missing banner")
	}
}

func TestSystemInfoWithStub(t *testing.T) {
	stub := &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		switch name {
		case "uname":
			switch args[0] {
			case "-r":
				return []byte("6.12.1-arch1-1\n"), nil, nil
			case "-m":
				return []byte("x86_64\n"), nil, nil
			}
		case "pacman":
			return []byte("Pacman v6.1.0 - libalpm v15.0.0\nCopyright\n"), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected %s %v", name, args)
	}}

	info, err := New(stub).SystemInfo(context.Background())
	if err != nil {
		t.Fatalf("SystemInfo() error = %v, want nil", err)
	}
	if info.Kernel != "6.12.1-arch1-1" {
		t.Errorf("Kernel = %q, want %q", info.Kernel, "6.12.1-arch1-1")
	}
	if info.Arch != "x86_64" {
		t.Errorf("Arch = %q, want %q", info.Arch, "x86_64")
	}
	if !info.Pacman.Available {
		t.Error("Pacman.Available = false, want true")
	}
	if info.Pacman.Version != "Pacman v6.1.0 - libalpm v15.0.0" {
		t.Errorf("Pacman.Version = %q, want first line", info.Pacman.Version)
	}
	// OS 字段读的是真机 /etc/os-release，内容因机器而异，只断言非空。
	// 用户字段来自 os/user 直调（不注入），同样只能放宽为非空——
	// 组合层的完整验收由设计会话的真机探针完成。
	if info.OSID == "" {
		t.Error("OSID = empty, want non-empty")
	}
	if info.OSPrettyName == "" {
		t.Error("OSPrettyName = empty, want non-empty")
	}
	if info.UserName == "" {
		t.Error("UserName = empty, want non-empty")
	}
	if info.Home == "" {
		t.Error("Home = empty, want non-empty")
	}
}

func TestSystemInfoTrimsCommandOutput(t *testing.T) {
	stub := &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		switch name {
		case "uname":
			switch args[0] {
			case "-r":
				return []byte("  6.12.1-arch1-1  \n\n"), nil, nil
			case "-m":
				return []byte("\tx86_64\n"), nil, nil
			}
		case "pacman":
			return []byte("Pacman v6.1.0 - libalpm v15.0.0\n"), nil, nil
		}
		return nil, nil, fmt.Errorf("unexpected %s %v", name, args)
	}}

	info, err := New(stub).SystemInfo(context.Background())
	if err != nil {
		t.Fatalf("SystemInfo() error = %v, want nil", err)
	}
	if info.Kernel != "6.12.1-arch1-1" {
		t.Errorf("Kernel = %q, want trimmed %q", info.Kernel, "6.12.1-arch1-1")
	}
	if info.Arch != "x86_64" {
		t.Errorf("Arch = %q, want trimmed %q", info.Arch, "x86_64")
	}
}

func TestSystemInfoUnameFailureIsError(t *testing.T) {
	stub := &stubExec{run: func(name string, args []string) ([]byte, []byte, error) {
		if name == "uname" && args[0] == "-r" {
			return nil, []byte("uname: broken\n"), errors.New("exit status 1")
		}
		return nil, nil, fmt.Errorf("unexpected %s %v", name, args)
	}}

	if _, err := New(stub).SystemInfo(context.Background()); err == nil {
		t.Fatal("SystemInfo() error = nil, want error")
	}
}
