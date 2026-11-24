package recorder

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Action 是对清单条目一次操作的动作分类，
// 将来直接映射到 MCP 信封的 action 字段。
type Action string

const (
	ActionAdded          Action = "added"
	ActionUpdated        Action = "updated"
	ActionUnchanged      Action = "unchanged"
	ActionRestored       Action = "restored"
	ActionRemoved        Action = "removed"
	ActionAlreadyRemoved Action = "already_removed"
	ActionNotPresent     Action = "not_present"
	ActionSet            Action = "set"
)

// 软件类型枚举（DESIGN.md 第 4 节）。type 是语义分类，系统无法推导，
// 只能由调用方声明，因此 Upsert 必须校验。
const (
	TypeDevTool = "devtool"
	TypeRuntime = "runtime"
	TypeService = "service"
	TypeOther   = "other"
)

// Facts 是“要记录的事实”。recorder 不关心这些值是谁查来的：
// 探查属于 observer，编排属于 executor；这里只收纯数据，
// 依赖方向因此永远是 executor → recorder，recorder 不 import observer。
type Facts struct {
	Type    string
	Version string
	Source  string
}

// Upsert 把一条事实全量应用到一个条目上，按条目当前状态分流：
//
//	不存在           → added     全字段写入，RecordedAt = CheckedAt = now
//	未移除、字段全同  → unchanged  一个字段都不动（CheckedAt 也不刷新）
//	未移除、任一不同  → updated    三字段更新，CheckedAt = now，RecordedAt 不变
//	已软删除          → restored   清 RemovedAt，三字段更新，RecordedAt 保持首次值
//
// name 为空或 type 不在枚举内返回错误，且 l 必须原样不动：
// 校验发生在任何修改之前，错误路径零副作用。
//
// 注意 unchanged 不做“内容没变就跳过写”的优化——那需要前后深拷贝对比，
// 复杂度不值得；revision 的语义是写计数器，不是内容指纹，
// 是否值得落盘由调用方结合 action 自行决定。
//
// Upsert 永不触碰 Config：add / unchanged / updated / restored 都不清空
// 已有的范围存档；软删后 restore，旧 scope 原样保留——判断不因删了
// 又加而失忆，和 RecordedAt 同一哲学。
func (l *List) Upsert(name string, f Facts, now time.Time) (Action, *Package, error) {
	// 校验先于一切修改：保证错误路径不碰 map、不改既有条目。
	if name == "" {
		return "", nil, fmt.Errorf("upsert: empty package name")
	}
	if !ValidType(f.Type) {
		return "", nil, fmt.Errorf("upsert %s: invalid type %q", name, f.Type)
	}
	// Load 之后的清单 Packages 永远非 nil；这里兜住手工构造的 List。
	if l.Packages == nil {
		l.Packages = map[string]*Package{}
	}

	p := l.Packages[name]
	switch {
	case p == nil:
		p = &Package{
			Name:       name,
			Source:     f.Source,
			Version:    f.Version,
			Type:       f.Type,
			RecordedAt: now,
			CheckedAt:  now,
		}
		l.Packages[name] = p
		return ActionAdded, p, nil

	case p.RemovedAt != nil:
		p.RemovedAt = nil
		p.Type = f.Type
		p.Version = f.Version
		p.Source = f.Source
		p.CheckedAt = now
		return ActionRestored, p, nil // RecordedAt 保持首次值

	case p.Type == f.Type && p.Version == f.Version && p.Source == f.Source:
		// 一个字段都不动，CheckedAt 也不刷新。
		return ActionUnchanged, p, nil

	default:
		p.Type = f.Type
		p.Version = f.Version
		p.Source = f.Source
		p.CheckedAt = now
		return ActionUpdated, p, nil // RecordedAt 不变
	}
}

// MarkRemoved 软删除：只打 removed_at 标记，不物理删除任何条目。
// Config 原样保留（软删不清存档，restore 后范围判断继续可用）。
//
//	不存在      → not_present
//	已软删除    → already_removed，不改任何字段
//	正常存在    → removed，RemovedAt = now
func (l *List) MarkRemoved(name string, now time.Time) Action {
	p := l.Packages[name]
	switch {
	case p == nil:
		return ActionNotPresent
	case p.RemovedAt != nil:
		return ActionAlreadyRemoved
	default:
		p.RemovedAt = &now
		return ActionRemoved
	}
}

// ValidType 报告 t 是否为合法的软件类型枚举。
//
// 双重身份：Upsert 在锁内用它执法（非法 type 拒绝写入）；
// executor 在探查之前引用同一函数做前置校验——两处引用同一份
// 实现，镜像漂移因此不可能发生。type 枚举扩容时只改这里。
func ValidType(t string) bool {
	switch t {
	case TypeDevTool, TypeRuntime, TypeService, TypeOther:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// 备份范围（Scope）存档
// ---------------------------------------------------------------------------

// Scope 是“备份范围判断”的存档：LLM 与用户对话的结果，PDP 只存不判。
// 校验的是结构与一致性（洞必须在墙里），不是“这个范围合不合理”。
type Scope struct {
	Include   bool        `json:"include"`
	Reason    string      `json:"reason,omitempty"` // include:false 时必填——“不备份”同样是判断
	Paths     []ScopePath `json:"paths,omitempty"`
	DecidedAt time.Time   `json:"decided_at"` // 判断达成时间；每次 SetScope 成功时刷新
}

// ScopePath 是备份范围中的一条路径取舍。
type ScopePath struct {
	Path   string `json:"path"`   // 必须绝对或以 ~ 开头
	Action string `json:"action"` // include | exclude
	Reason string `json:"reason"` // 逐条必填——预设的缺席由可审查性补偿
}

// ScopePath 的 action 词表。注意与 Action（清单操作动作）是两个词表：
// include / exclude 描述路径的取舍，Action 描述对清单条目的动作。
// ScopePath.Action 因此是 string 而非 Action——混型反而危险。
const (
	ActionInclude = "include"
	ActionExclude = "exclude"
)

// ErrInvalidScope 表示 scope 校验失败（缺 reason、洞在墙外、软删条目等）。
// mcpserver 映射为信封 status=rejected。
//
// 理由走 ValidType 的老结论：校验只有一份、活在执法者那里；
// SetScope 没有探查可省，executor 预检毫无意义——哨兵让错误原样
// 上抛也能在信封层被正确分类。
var ErrInvalidScope = errors.New("invalid scope")

// SetScope 存档一个软件的备份范围判断，校验全部发生在修改之前
// （老规矩：错误路径零副作用）。
// 所有校验与状态错误都包 ErrInvalidScope（errors.Is 判别），
// 含软删条目——DESIGN 定它为 rejected，它是状态不是故障：
//
//	name 空                            → error
//	条目不存在                          → ActionNotPresent（无 error，handler 映射 not_found）
//	条目已软删                          → error（先恢复条目再谈范围）
//	include:false 且顶层 Reason 空       → error
//	include:false 但带 paths             → error（不备份却给路径清单，与下一条对称的自相矛盾）
//	include:true 但没有一条 include 路径 → error（等于备份空集）
//	任一路径：空 / 相对 / action 非法 / reason 空 → error
//	exclude 不严格落在某个 include 之下   → error（洞必须在墙里）
//	同一路径（Clean 后）出现两次          → error（歧义；exclude 等于某条 include
//	                                      也会先在这里被拦下——一个路径不能既是墙又是洞）
//
// 通过后写 p.Config，返回 ActionSet。DecidedAt = now 写在存档那份
// 拷贝上（判断是“最新一次对话的结果”，每次成功刷新）；不碰
// CheckedAt（它是“与系统核对”的时间戳，scope 是对话存档，不是核对），
// RecordedAt 亦不动。
func (l *List) SetScope(name string, sc Scope, now time.Time) (Action, error) {
	// 校验先于一切修改：错误路径不碰 map、不改既有条目。
	if name == "" {
		return "", fmt.Errorf("%w: set scope: empty package name", ErrInvalidScope)
	}
	p := l.Packages[name]
	if p == nil {
		// 同 MarkRemoved 模式：不存在的条目是业务结果，不是 error。
		return ActionNotPresent, nil
	}
	if p.RemovedAt != nil {
		return "", fmt.Errorf("%w: set scope %s: entry is soft-removed; restore it before setting scope", ErrInvalidScope, name)
	}

	if !sc.Include {
		if sc.Reason == "" {
			return "", fmt.Errorf("%w: set scope %s: include=false requires a reason (\"not backing up\" is a decision too)", ErrInvalidScope, name)
		}
		if len(sc.Paths) > 0 {
			return "", fmt.Errorf("%w: set scope %s: include=false takes no paths (nothing to back up)", ErrInvalidScope, name)
		}
	} else {
		hasInclude := false
		for _, sp := range sc.Paths {
			if sp.Action == ActionInclude {
				hasInclude = true
				break
			}
		}
		if !hasInclude {
			return "", fmt.Errorf("%w: set scope %s: include=true requires at least one include path (empty backup set)", ErrInvalidScope, name)
		}
	}

	// 逐条校验，Clean 后收集：重复与洞-墙判断都以规范化路径为准。
	cleaned := make(map[string]bool, len(sc.Paths))
	includes := make([]string, 0, len(sc.Paths))
	excludes := make([]string, 0, len(sc.Paths))
	for _, sp := range sc.Paths {
		if sp.Path == "" {
			return "", fmt.Errorf("%w: set scope %s: empty path", ErrInvalidScope, name)
		}
		if !filepath.IsAbs(sp.Path) && !strings.HasPrefix(sp.Path, "~") {
			return "", fmt.Errorf("%w: set scope %s: path %q must be absolute or start with ~", ErrInvalidScope, name, sp.Path)
		}
		if sp.Action != ActionInclude && sp.Action != ActionExclude {
			return "", fmt.Errorf("%w: set scope %s: path %q has invalid action %q (want %q or %q)",
				ErrInvalidScope, name, sp.Path, sp.Action, ActionInclude, ActionExclude)
		}
		if sp.Reason == "" {
			return "", fmt.Errorf("%w: set scope %s: path %q requires a reason", ErrInvalidScope, name, sp.Path)
		}
		c := filepath.Clean(sp.Path)
		if cleaned[c] {
			return "", fmt.Errorf("%w: set scope %s: duplicate path %q", ErrInvalidScope, name, sp.Path)
		}
		cleaned[c] = true
		if sp.Action == ActionInclude {
			includes = append(includes, c)
		} else {
			excludes = append(excludes, c)
		}
	}

	// 洞必须在墙里：exclude 必须严格落在某个 include 之下。
	// 前缀判断带分隔符结尾——“等于 include 本身”与“/a/b 命中 /a/bc”
	// 这两个退化情形都被同一把尺子排除。
	for _, e := range excludes {
		inWall := false
		for _, i := range includes {
			if strings.HasPrefix(e, i+string(filepath.Separator)) {
				inWall = true
				break
			}
		}
		if !inWall {
			return "", fmt.Errorf("%w: set scope %s: exclude %q is not inside any include path", ErrInvalidScope, name, e)
		}
	}

	// 写入（校验全过才到达这里）。防御性拷贝 Paths：存档不与调用方的
	// 切片共享底层数组，调用方事后改自己的切片动不了已存档的判断。
	// DecidedAt 写在存档副本上——调用方的 sc 保持干净，也不受影响。
	stored := sc
	if len(sc.Paths) > 0 {
		stored.Paths = append([]ScopePath(nil), sc.Paths...)
	}
	stored.DecidedAt = now
	p.Config = &stored
	return ActionSet, nil
}
