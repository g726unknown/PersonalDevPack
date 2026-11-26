// Package snapper 是快照引擎：把存档的备份范围判断变成真实快照。
//
// 包名消歧：与 Btrfs 快照管理工具 snapper 无关——本包是 PDP 自己的
// 单快照引擎，名字按施动者家族取（observer / recorder / pathobserver
// 之后），词表之争到此为止。
//
// 全项目唯一允许 Open / WriteFile 用户文件的包——第三块牌坊：
// observer 的白名单管命令，pathobserver 的白名单管元数据 syscall，
// 这里的约束是【写只进 PDP 自己的领地】：读用户文件是为了复制，
// 落点永远在 root（PDP_HOME/backups）之下；PDP 永不写机器侧
// （DESIGN §5）。内容只走程序字节管道（io.Copy），落进模型上下文
// 的只有路径与哈希——复制即哈希，一遍过。
//
// 忠实执行存档（阶段三拍板）：墙内减洞、按链接复制、底线照常
// 复制但在 FloorIncluded 点名——先报告，再决断；存档没说清的，
// 报告而不猜测。
//
// 单快照（拍板修订：PDP 不是版本管理工具）：快照是存档判断的
// 投影，不是历史账本；每次成功 sync 顶掉上一份，只保留最新一个。
// 用户要历史版本，让 LLM 维护多份清单（或多个 PDP_HOME）。
package snapper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// snapshotTimeLayout 是快照 id 的时间戳格式（UTC）。
const snapshotTimeLayout = "20060102T150405Z"

// Include 是一条 include。Explicit 区分“scope 里点名的条目”与
// “目录展开扫到的文件”——底线报告的分界依据（DESIGN §4：先报告
// 再决断：显式条目 = 用户已确认；扫到的命中要点名交回决断）。
type Include struct {
	Path     string // 绝对路径（~ 已由 executor 展开）
	Explicit bool
}

// PkgPlan 是一个软件的复制计划——中性的输入，scope→plan 的判断
// 消费在 executor（卡 #16）。本包不认识 Scope，只执行计划。
type PkgPlan struct {
	Name     string
	Includes []Include
	Excludes []string // 绝对路径，严格后代
}

// Engine 执行快照；root 是备份根（PDP_HOME/backups），home 用于
// 底线 pattern 的 ~ 展开。
type Engine struct{ root, home string }

// New 返回指向 root 的快照引擎。
func New(root, home string) *Engine { return &Engine{root: root, home: home} }

// FileEntry 是 manifest 里的一个文件记录。
type FileEntry struct {
	Path     string    `json:"path"` // 绝对源路径
	Size     int64     `json:"size"`
	Mode     string    `json:"mode"` // 权限位八进制，如 "0644"
	MTime    time.Time `json:"mtime"`
	SHA256   string    `json:"sha256"`
	Target   string    `json:"symlink_target,omitempty"` // 按链接复制，存目标字符串
	Broken   bool      `json:"symlink_broken,omitempty"`
	Floor    bool      `json:"floor,omitempty"`
	Explicit bool      `json:"explicit"`
}

// Manifest 是快照的人可读账本（DESIGN §5）：每个文件的路径、
// SHA-256、大小、mtime、symlink 目标，以及来源清单的 revision。
type Manifest struct {
	SnapshotID string      `json:"snapshot_id"` // UTC：20060102T150405Z；同秒冲突加 -2 后缀
	CreatedAt  time.Time   `json:"created_at"`
	ListRev    int         `json:"list_revision"`
	Files      []FileEntry `json:"files"`
}

// DiffStats 是机器 ↔ 上一次快照的逐文件比对计数。
type DiffStats struct{ Added, Modified, Deleted int }

// Result 是一次 Sync 的输出。
type Result struct {
	SnapshotID    string // dryRun 时为空
	Diff          DiffStats
	FloorIncluded []string // 复制了的、非显式条目的底线命中——先报告，再决断
	SkippedError  []string
	Warnings      []string
	FilesTotal    int
}

// mirror 把绝对路径映射为快照内的相对布局：去掉开头的分隔符。
// 人拿文件管理器打开快照目录就能对上源路径（DESIGN §5 的布局理由）。
func mirror(abs string) string {
	return strings.TrimPrefix(abs, string(filepath.Separator))
}

// snapshotIDRe 识别快照目录名（.tmp- 与无关文件不入替换视野）。
var snapshotIDRe = regexp.MustCompile(`^\d{8}T\d{6}Z(-\d+)?$`)

// snapshotIDs 返回 root 下现有快照 id，升序。
// id 是 UTC 时间戳格式，字典序即时间序；同秒 -2 后缀天然排在基名之后。
func (e *Engine) snapshotIDs() ([]string, error) {
	entries, err := os.ReadDir(e.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read backup root %s: %w", e.root, err)
	}
	ids := []string{}
	for _, en := range entries {
		if en.IsDir() && snapshotIDRe.MatchString(en.Name()) {
			ids = append(ids, en.Name())
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// unusedID 确保快照 id 不与现有目录冲突：同秒重入加 -2、-3… 后缀
// （16 号测试场景：测试与真实环境都可能同秒两次 sync）。
func (e *Engine) unusedID(base string) (string, error) {
	if _, err := os.Stat(filepath.Join(e.root, base)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return base, nil
		}
		return "", fmt.Errorf("stat snapshot dir: %w", err)
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d", base, n)
		_, err := os.Stat(filepath.Join(e.root, cand))
		if errors.Is(err, fs.ErrNotExist) {
			return cand, nil
		}
		if err != nil {
			return "", fmt.Errorf("stat snapshot dir: %w", err)
		}
	}
}

// ErrNoSnapshot 表示 backups 下还没有任何快照
// （资源层映射为 MCP 的“资源不存在”语义，不是错误）。
var ErrNoSnapshot = errors.New("no snapshot yet")

// Current 返回当前（最新）快照的 manifest。
// 没有任何快照时返回 ErrNoSnapshot——首次 sync 前的合法状态，
// 调用方（资源层）据此回答 not found。
//
// ctx 目前不参与（单文件读），仅为保留接口形状。
func (e *Engine) Current(ctx context.Context) (*Manifest, error) {
	ids, err := e.snapshotIDs()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNoSnapshot
	}
	path := filepath.Join(e.root, ids[len(ids)-1], "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &m, nil
}

// latestManifest 读现有最新快照的 manifest；一个快照都没有时返回
// (nil, nil)——首次 sync 是合法状态，diff 的 prev 全零。
func (e *Engine) latestManifest() (*Manifest, error) {
	ids, err := e.snapshotIDs()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	path := filepath.Join(e.root, ids[len(ids)-1], "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &m, nil
}

// diffStats 比对上一次快照与本次收集：prev 没有的 = added，
// 路径同而哈希异 = modified，prev 有而本次没走到的 = deleted
// （scope 收缩或 exclude 增加由此体现）。
func diffStats(prev *Manifest, cur map[string]FileEntry) DiffStats {
	var d DiffStats
	if prev == nil {
		d.Added = len(cur) // 首次 sync：全部都是新增
		return d
	}
	prevByPath := make(map[string]FileEntry, len(prev.Files))
	for _, f := range prev.Files {
		prevByPath[f.Path] = f
	}
	for path, c := range cur {
		p, ok := prevByPath[path]
		switch {
		case !ok:
			d.Added++
		case p.SHA256 != c.SHA256:
			d.Modified++
		}
	}
	for path := range prevByPath {
		if _, ok := cur[path]; !ok {
			d.Deleted++
		}
	}
	return d
}
