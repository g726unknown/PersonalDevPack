package observer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// defaultOSReleasePath 是 freedesktop os-release 的规范位置，
// SystemInfo 以它作为生产路径。
// readOSRelease 按参数取路径而不是写死，测试才能用不存在的
// 路径单独打读路径（解析规则另由 parseOSRelease + testdata 验证）。
const defaultOSReleasePath = "/etc/os-release"

// PacmanInfo 是 pacman 可用性的事实。
// Available=false 是合法状态：没有 pacman 的机器上 system_info
// 仍要能回答其余问题，不可用不是错误。
type PacmanInfo struct {
	Available bool
	Version   string // 版本横幅 "Pacman v7.1.0 - libalpm v16.0.1"（首行带 ASCII logo，见 pacmanVersionLine）
}

// SystemInfo 是 pdp_system_info 的全部观察性事实。
// 信封里的 paths.state_dir 与 capabilities[] 不在其中：
// 它们来自装配层（main 解析 PDP_HOME、注册工具），
// 不是观察来的——观察者只报事实。
type SystemInfo struct {
	OSID         string // os-release 的 ID: "arch"
	OSPrettyName string // PRETTY_NAME: "Arch Linux"
	OSVersionID  string // VERSION_ID；滚动发行版为空——空是事实，不是错误
	Kernel       string // uname -r
	Arch         string // uname -m
	UserName     string
	Home         string
	Pacman       PacmanInfo
}

// PacmanVersion 探查 pacman 是否可用及其版本号。
//
// 不可用不是错误：exec.ErrNotFound → PacmanInfo{Available:false}, nil，
// 没有 pacman 的机器上 system_info 照样要能回答其余问题；
// 其他失败才是 error。空输出同样报 error：退出 0 却一个字不说，
// 不允许静默报成“可用但版本未知”。
func (o *Observer) PacmanVersion(ctx context.Context) (PacmanInfo, error) {
	out, _, err := o.exec.Run(ctx, "pacman", "--version")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return PacmanInfo{Available: false}, nil
		}
		return PacmanInfo{}, fmt.Errorf("pacman --version: %w", err)
	}
	v, ok := pacmanVersionLine(out)
	if !ok {
		return PacmanInfo{}, errors.New("pacman --version: no version banner found")
	}
	return PacmanInfo{Available: true, Version: v}, nil
}

// SystemInfo 组合四条取数路线：
//
//	os-release → 读文件 + 纯解析，不起子进程（比跑命令少一个依赖）
//	uname -r / uname -m → 走 Execer
//	pacman --version → 走 Execer
//	os/user.Current → stdlib 直调，不注入
//
// os/user 不注入是刻意的：为它造假实现属于“为可测而可测”——
// stdlib 稳定、单一事实源。代价要诚实记录：组合层因此无法
// 完全 stub 测试，OS 与用户字段的测试断言放宽为非空，
// 完整验收靠真机探针。
func (o *Observer) SystemInfo(ctx context.Context) (SystemInfo, error) {
	raw, err := readOSRelease(defaultOSReleasePath)
	if err != nil {
		return SystemInfo{}, err
	}
	id, pretty, versionID := parseOSRelease(raw)

	// 不合并成 uname -rm：单字段输出没有解析歧义，
	// 也不依赖对“flag 顺序如何影响输出顺序”的记忆。
	krOut, _, err := o.exec.Run(ctx, "uname", "-r")
	if err != nil {
		return SystemInfo{}, fmt.Errorf("uname -r: %w", err)
	}
	kmOut, _, err := o.exec.Run(ctx, "uname", "-m")
	if err != nil {
		return SystemInfo{}, fmt.Errorf("uname -m: %w", err)
	}

	pv, err := o.PacmanVersion(ctx)
	if err != nil {
		return SystemInfo{}, err
	}

	u, err := user.Current()
	if err != nil {
		return SystemInfo{}, fmt.Errorf("current user: %w", err)
	}

	return SystemInfo{
		OSID:         id,
		OSPrettyName: pretty,
		OSVersionID:  versionID,
		Kernel:       strings.TrimSpace(string(krOut)),
		Arch:         strings.TrimSpace(string(kmOut)),
		UserName:     u.Username,
		Home:         u.HomeDir,
		Pacman:       pv,
	}, nil
}

// readOSRelease 读取 path 的 os-release 内容。
// 文件缺失必须报错：目标环境恒有此文件，静默降级会让 system_info
// 的输出看起来正常、其实缺了核心事实。
func readOSRelease(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return raw, nil
}

// parseOSRelease 解析 freedesktop KEY=VALUE 格式，纯函数、零 IO：
// 按行拆、首个 = 分割（值里可以有 =）、剥离首尾引号；
// # 注释行与无 = 的畸形行忽略。
func parseOSRelease(raw []byte) (id, pretty, versionID string) {
	for line := range strings.SplitSeq(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ID":
			id = unquote(val)
		case "PRETTY_NAME":
			pretty = unquote(val)
		case "VERSION_ID":
			versionID = unquote(val)
		}
	}
	return id, pretty, versionID
}

// unquote 剥离 os-release 规范允许的首尾双引号（恰好一对）。
func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// pacmanVersionLine 在 pacman --version 的输出里定位版本横幅。
// 真机首行是 " .--.      Pacman v7.1.0 - libalpm v16.0.1"——版本号
// 不独占一行，直接取首行会把 ASCII logo 一起带回来（真机探针纠正的
// 假设，stub 永远测不出来）。
// "Pacman v" 横幅本身不受 locale 影响——本地化的只是版权行。
func pacmanVersionLine(out []byte) (string, bool) {
	for line := range strings.SplitSeq(string(out), "\n") {
		if i := strings.Index(line, "Pacman v"); i >= 0 {
			return strings.TrimSpace(line[i:]), true
		}
	}
	return "", false
}
