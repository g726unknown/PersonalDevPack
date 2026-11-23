package observer

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// OSExecer 是 Execer 的生产实现。
//
// 用 exec.CommandContext 而不是 exec.Command：前者把 ctx 取消传给
// 子进程，用户中断会话时正在跑的 pacman 会被杀掉，不留下失控进程。
// args 以切片直传、不经过 shell，天然没有命令注入问题。
type OSExecer struct{}

// Run 执行命令并收集输出：
//   - 成功：返回 stdout，stderr 为 nil；
//   - 非零退出：err 是 *exec.ExitError，stderr 藏在其中，这里拆出透传；
//   - 二进制不存在：err 满足 errors.Is(err, exec.ErrNotFound)。
//
// 子进程环境强制 LC_ALL=C：所有对命令输出的文本判断
// （pacman.go 里对 "was not found" 的嗅探等）都假定英文，而用户的
// locale 可能是任何语言——本机实测为中文，错误分类当场失效。
// LC_ALL 在 libc 的 locale 优先级中最高，覆盖 LANG 等一切设置。
// 这是唯一创建子进程的地方，在此固定 locale，所有调用点自动确定。
func (OSExecer) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// os.Environ() 里可能已有用户设置的 LC_ALL；glibc 的 getenv
	// 取第一条命中，简单 append 会被旧值抢先，所以先剔除再追加。
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "LC_ALL=")
	}), "LC_ALL=C")

	stdout, err := cmd.Output()
	if err == nil {
		return stdout, nil, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout, ee.Stderr, err
	}
	return stdout, nil, err
}
