// Package observer 提供系统与包管理器的只读事实。
//
// 本包只报事实、不推导 source：“foreign → aur、未装 → unknown”这层
// 翻译属于 executor。将来换 apt：observer 换实现、翻译规则换地方，
// 互不拖累。
package observer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// PkgStatus 是对一个包名探查后的事实，三个字段都是原始事实。
// source 的推导不在这里，见包注释。
type PkgStatus struct {
	Name      string
	Installed bool
	Version   string // "name version" 的第二列
	Foreign   bool   // 在 -Qm 输出中命中
}

// PacmanQuery 查询 names 的安装状态，只读、不写任何状态。
//
// 流程：输入检查（空输入不跑命令；空串拒绝）→ 去重保序 →
// pacman -Q <names...> 拿已安装与版本 → pacman -Qm 一次全量拿
// foreign 集合 → 组装。
//
// 错误分类：
//   - pacman 二进制不存在：errors.Is(err, exec.ErrNotFound) 为真；
//   - -Q 非零退出且 stderr 含 "was not found"：部分名字没装，不是错误；
//   - -Q 非零退出且 stderr 是别的内容：真实错误；
//   - -Qm 空 stderr 的非零退出：查询无结果（机器上没有 foreign 包），
//     不是错误；非空 stderr 才上报。
//
// "was not found" 是英文文本嗅探，脆弱，但 pacman 没有机器可读的替代；
// 文案变化时这里会把查询判为真实错误——宁误报，不误吞。
func (o *Observer) PacmanQuery(ctx context.Context, names []string) ([]PkgStatus, error) {
	// 输入检查在一切命令之前：空输入零命令，空串拒绝而不静默丢弃。
	if len(names) == 0 {
		return nil, nil
	}
	if slices.Contains(names, "") {
		return nil, errors.New("pacman query: empty package name in input")
	}

	// 去重，保留首次出现的顺序。
	seen := make(map[string]bool, len(names))
	uniq := make([]string, 0, len(names))
	for _, n := range names {
		if !seen[n] {
			seen[n] = true
			uniq = append(uniq, n)
		}
	}

	// pacman -Q：一条命令查所有名字。
	qOut, qErrOut, err := o.exec.Run(ctx, "pacman", append([]string{"-Q"}, uniq...)...)
	if err != nil && !partialNotFound(qErrOut) {
		// 二进制不存在（exec.ErrNotFound）与其他真实故障都从这里上报：
		// 它们的 stderr 都不是 "was not found" 的形状。
		return nil, fmt.Errorf("pacman -Q: %w", err)
	}
	installed := parseNameVersion(qOut)

	// pacman -Qm：一次全量拿 foreign 名单，不逐包查。
	mOut, mErrOut, err := o.exec.Run(ctx, "pacman", "-Qm")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || len(bytes.TrimSpace(mErrOut)) > 0 {
			return nil, fmt.Errorf("pacman -Qm: %w", err)
		}
		// 走到这里是“空 stderr 的非零退出”：机器上没有 foreign 包，
		// 查询无结果，不是故障。
	}
	foreign := parseNameVersion(mOut)

	statuses := make([]PkgStatus, 0, len(uniq))
	for _, n := range uniq {
		st := PkgStatus{Name: n}
		if v, ok := installed[n]; ok {
			st.Installed = true
			st.Version = v
			if _, ok := foreign[n]; ok {
				st.Foreign = true
			}
		}
		statuses = append(statuses, st)
	}
	return statuses, nil
}

// partialNotFound 报告 stderr 是否是“部分包未找到”的形状。
//
// 依赖 pacman 的英文错误文案——而英文由 OSExecer 的 LC_ALL=C 保证
// （见 exec.go）：用户 locale 可能是任何语言，本补丁前中文环境下
// 这里的嗅探失效，“部分未安装”被整批误判为真实错误。
// 文案变化时这里会把查询判为真实错误——宁误报，不误吞。
func partialNotFound(stderr []byte) bool {
	return bytes.Contains(stderr, []byte("was not found"))
}

// parseNameVersion 解析 "name version" 行为 map。
// strings.Fields 后长度恰为 2 才采纳；空行与字段数不符的行忽略、
// 不影响其他行——pacman 输出受控，防御性解析不花成本。
func parseNameVersion(out []byte) map[string]string {
	m := make(map[string]string)
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		m[f[0]] = f[1]
	}
	return m
}
