// Package pathobserver 探查文件系统路径的元数据事实。
//
// 包名认 observer 为家族（同族只读事实、平级并列、都被 executor 持有），
// path 前缀划定领域：observer 探查外部进程（pacman，命令输出本身就是
// 给 LLM 看的证据），本包探查文件系统——内容永不进入模型上下文。
// 两个探查面的内容政策相反，因此牌坊各自立在包级（见下）。
//
// 定位：备份范围判断的唯一证据来源，不外包给宿主 agent 的 shell / read。
// 三个理由，各是一条合同：内容边界只在“结构性读不了内容”的工具里成立
// （宿主工具不受 PDP 约束，PDP 自己的通道必须管住）；敏感底线需要唯一
// 检查点（宿主工具不认识 Floor）；PDP 承诺接任何 MCP 客户端——裸客户
// 端没有宿主行李可依赖。
//
// 硬边界：本包永不读取文件内容。允许的 syscall 白名单只有四个——
// os.Lstat、os.Stat（仅判定 symlink 目标是否存在，不取任何字段）、
// os.Readlink、os.ReadDir（只取 Name()）。没有 Open，没有 ReadFile。
// 文件内容永不进入模型上下文（DESIGN §1），这里是这条边界的执法现场：
// 将来谁想“顺手加个读内容的方法”，先撞见这块牌子。
//
// 探查不递归（目录只看第一层）、不穿透（symlink 只报链接与目标）：
// inspect 的用途是给“备份范围判断”提供存在性与规模的证据；
// 完整清单与链接跟随策略是备份阶段（阶段三）的决定，探查工具不预支。
package pathobserver

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Facts.Kind 的合法值枚举。
const (
	KindFile    = "file"
	KindDir     = "dir"
	KindSymlink = "symlink"
	KindOther   = "other" // 设备、socket、fifo 等
)

// defaultMaxEntries 是 Input.MaxEntries 为 0 时的目录采样上限。
const defaultMaxEntries = 50

// Facts 是一条路径的元数据事实。单条失败只写 Error，不拖垮整批。
// Path 原样回显探查时收到的路径（~ 不展开）——LLM 问什么答什么。
type Facts struct {
	Path             string     `json:"path"`
	Exists           bool       `json:"exists"`
	Kind             string     `json:"kind"`
	Mode             string     `json:"mode,omitempty"`           // 权限位的八进制，如 "0644"
	Size             int64      `json:"size,omitempty"`           // 仅 regular file：精确字节数
	MTime            *time.Time `json:"mtime,omitempty"`          // 仅 Exists 条目出现；不存在/失败不携带零时间噪音
	SymlinkTarget    string     `json:"symlink_target,omitempty"` // 解析为绝对路径的链接目标
	SymlinkBroken    bool       `json:"symlink_broken,omitempty"`
	EntriesSample    []string   `json:"entries_sample,omitempty"` // 目录第一层，名字序
	EntriesTotal     int        `json:"entries_total,omitempty"`
	Truncated        bool       `json:"truncated"`                   // 采样被 MaxEntries 截断
	SensitiveFloor   bool       `json:"sensitive_floor"`             // 路径自身命中敏感底线
	SensitiveEntries []string   `json:"sensitive_entries,omitempty"` // 采样子名字命中底线
	Error            string     `json:"error,omitempty"`             // 单条失败原因，不拖垮整批
}

// Input 是一次探查请求。
type Input struct {
	Path       string
	MaxEntries int // 目录采样上限；0 → defaultMaxEntries
}

// Pattern 是敏感底线的一条匹配规则：Glob 按展开后的完整路径匹配，
// Suffix 按文件名后缀匹配；NotSuffix 是命中后的豁免——文件名以它
// 结尾则放行。二选一匹配，豁免可选。
//
// 豁免的依据是 SSH 的命名约定：私钥 X、公钥 X.pub，外科手术级精确，
// 一条 NotSuffix 就把误拦面切干净。底线权威性来自精确：拦下公钥是在
// 训练用户无视警告（狼来了效应），而阶段三的物理拒绝会真的拒掉公钥
// 备份——公钥恰恰是用户想带去新机器的东西（authorized_keys、恢复场景）。
type Pattern struct {
	Glob      string
	Suffix    string
	NotSuffix string
}

// Floor 是敏感硬底线（DESIGN §4）：用户可查看、可修改（修改入口后续
// 阶段提供）。永不静默进入备份；本包只在元数据层标记，不读取内容、
// 不阻止 LLM 表达意见——物理拒绝是备份阶段（阶段三）的事。
//
// pattern 里的 ~ 按本 Prober 的 home 展开，测试注入临时目录同样成立。
var Floor = []Pattern{
	{Glob: "~/.ssh/id_*", NotSuffix: ".pub"}, // 拦 X，放 X.pub——SSH 的私钥/公钥命名约定
	{Glob: "~/.netrc"},
	{Glob: "~/.pgpass"},
	{Glob: "~/.aws/credentials"},
	{Suffix: ".pem"}, // 按文件名后缀匹配
}

// Prober 探查路径元数据；home 用于展开 ~ 与底线 pattern。
type Prober struct{ home string }

// New 返回以 home 展开 ~ 的 Prober。
func New(home string) *Prober { return &Prober{home: home} }

// Probe 批量探查，一问一答各条独立：单条失败只写进那条的 Error，
// 其余条目照常——批量工具的诚实形态是把失败标在结果里，
// 而不是让一条权限问题废掉整个调用。
//
// ctx 目前不参与（每条几个 syscall，快到无需取消），仅为将来保留接口形状。
func (p *Prober) Probe(ctx context.Context, in []Input) []Facts {
	out := make([]Facts, 0, len(in))
	for _, item := range in {
		out = append(out, p.probeOne(item))
	}
	return out
}

// probeOne 探查单条路径。错误两种形态：
// 不存在 → Exists:false 无 Error（事实，不是故障）；
// 探查失败（权限等）→ Error 非空、Exists:false（单条自保，不拖垮整批）。
func (p *Prober) probeOne(in Input) Facts {
	f := Facts{Path: in.Path}

	expanded, err := p.expand(in.Path)
	if err != nil {
		// 防御性自保：相对路径拒绝探查。信封层的 rejected 是
		// executor 预检的职责（卡 #13），这里只如实标注。
		f.Error = err.Error()
		return f
	}

	fi, err := os.Lstat(expanded)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return f // 不存在是事实，不是故障
		}
		f.Error = err.Error()
		return f
	}

	f.Exists = true
	f.Mode = fmt.Sprintf("%04o", uint32(fi.Mode().Perm()))
	// 先落局部变量再取地址：函数返回值不可寻址，&fi.ModTime() 编译不过。
	mtime := fi.ModTime()
	f.MTime = &mtime
	f.SensitiveFloor = MatchFloor(expanded, p.home)

	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		// 不穿透：报链接本身与解析后的目标，不列目标内容——
		// “这是个指向 X 的链接”是 LLM 需要的事实，X 的内部不是。
		f.Kind = KindSymlink
		target, err := os.Readlink(expanded)
		if err != nil {
			f.Error = err.Error()
			return f
		}
		if filepath.IsAbs(target) {
			f.SymlinkTarget = filepath.Clean(target)
		} else {
			// 相对目标按链接所在目录拼成绝对路径：报给 LLM 的事实
			// 应当无歧义（“相对于谁”不该是隐式知识）。
			f.SymlinkTarget = filepath.Join(filepath.Dir(expanded), target)
		}
		// os.Stat 在白名单内的唯一用途：判定目标存在与否。
		if _, err := os.Stat(f.SymlinkTarget); err != nil {
			f.SymlinkBroken = true
		}

	case fi.IsDir():
		f.Kind = KindDir
		max := in.MaxEntries
		if max <= 0 {
			max = defaultMaxEntries
		}
		entries, err := os.ReadDir(expanded)
		if err != nil {
			// Lstat 看得到门牌、ReadDir 进不了门（常见：目录无 x 位）。
			f.Error = err.Error()
			return f
		}
		f.EntriesTotal = len(entries)
		// os.ReadDir 保证按文件名排序，采样即有序前缀。
		for i, e := range entries {
			if i == max {
				f.Truncated = true
				break
			}
			// 只取 Name()——白名单规则。
			f.EntriesSample = append(f.EntriesSample, e.Name())
			if MatchFloor(filepath.Join(expanded, e.Name()), p.home) {
				f.SensitiveEntries = append(f.SensitiveEntries, e.Name())
			}
		}

	case fi.Mode().IsRegular():
		f.Kind = KindFile
		f.Size = fi.Size()

	default:
		f.Kind = KindOther
	}
	return f
}

// expand 把 ~ 与 ~/ 前缀按 home 展开，并拒绝相对路径。
// ~user 形式不在展开之列——那是别人的家，探查者无权假设其位置；
// 它会落入相对路径拒绝分支。
func (p *Prober) expand(path string) (string, error) {
	switch {
	case path == "~":
		return p.home, nil
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(p.home, path[2:]), nil
	case filepath.IsAbs(path):
		return path, nil
	}
	return "", fmt.Errorf("pathobserver: relative path %q requires an absolute or ~/ prefix", path)
}

// MatchFloor 报告绝对路径 absPath 是否命中敏感底线。
// absPath 须为展开后的绝对路径（~ 已解析）；home 用于展开
// 底线 pattern 里的 ~ 前缀。
//
// 包内 Prober 与阶段三的 backup 引擎共用这一份判定——
// 底线只有一份词表、一个判定者。
func MatchFloor(absPath, home string) bool {
	for _, pat := range Floor {
		var hit bool
		switch {
		case pat.Glob != "":
			if ok, err := filepath.Match(expandPattern(pat.Glob, home), absPath); err == nil && ok {
				hit = true
			}
		case pat.Suffix != "":
			if strings.HasSuffix(filepath.Base(absPath), pat.Suffix) {
				hit = true
			}
		}
		if !hit {
			continue
		}
		if pat.NotSuffix != "" && strings.HasSuffix(filepath.Base(absPath), pat.NotSuffix) {
			continue // 豁免：拦 X，放 X.pub——公钥不是秘密
		}
		return true
	}
	return false
}

// expandPattern 把底线 pattern 的 ~ 前缀按 home 展开。
// Floor 是包级常量，形态永远合法，不需要错误路径。
func expandPattern(pattern, home string) string {
	switch {
	case pattern == "~":
		return home
	case strings.HasPrefix(pattern, "~/"):
		return filepath.Join(home, pattern[2:])
	}
	return pattern
}
