package executor

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/g726unknown/pdp/internal/recorder"
)

// ShowEntry 是 ShowList 输出里的一个条目（未移除）。
// Config 为 nil 表示尚未讨论过备份范围（≠ include:false 的明确不备份）。
type ShowEntry struct {
	Name       string
	Source     string
	Version    string
	Type       string
	RecordedAt time.Time
	CheckedAt  time.Time
	Config     *recorder.Scope
}

// ShowResult 是 ShowList 的输出。
// Total 只数未移除条目；软删条目计入 RemovedCount、不进 Entries。
type ShowResult struct {
	Revision     int
	Total        int // 未移除条目数
	ByType       map[string]int
	Entries      []ShowEntry
	RemovedCount int // 软删条目数
}

// ShowList 只读呈现清单，唯一动作是 rec.Load——完全不探查系统。
//
// 空形态约定：Entries 非 nil 空 slice、ByType 非 nil 空 map。
// 下游信封序列化时 nil 会被 omitempty 吞掉或渲染成 null，
// 字段形态不稳定会让 LLM 看到两种 schema。
func (s *Service) ShowList(ctx context.Context) (ShowResult, error) {
	l, err := s.rec.Load(ctx)
	if err != nil {
		return ShowResult{}, err
	}

	entries := make([]ShowEntry, 0, len(l.Packages))
	byType := make(map[string]int, len(l.Packages))
	removed := 0
	for name, p := range l.Packages {
		if p.RemovedAt != nil {
			removed++
			continue
		}
		entries = append(entries, ShowEntry{
			Name:       name,
			Source:     p.Source,
			Version:    p.Version,
			Type:       p.Type,
			RecordedAt: p.RecordedAt,
			CheckedAt:  p.CheckedAt,
			Config:     p.Config, // 指针直传：nil 与已判断两种状态原样上抛
		})
		byType[p.Type]++
	}

	// map 遍历顺序被运行时故意随机化。排序不是美观问题，缺了它
	// 每次调用的条目顺序都不同——对 LLM 是噪音、对客户端缓存是
	// 灾难、对测试是“偶尔红”，三重理由缺一不可。
	slices.SortFunc(entries, func(a, b ShowEntry) int {
		return strings.Compare(a.Name, b.Name)
	})

	return ShowResult{
		Revision:     l.Revision,
		Total:        len(entries),
		ByType:       byType,
		Entries:      entries,
		RemovedCount: removed,
	}, nil
}
