package recorder

import (
	"fmt"
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
