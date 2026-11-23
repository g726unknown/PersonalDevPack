package executor

import (
	"context"
	"slices"
)

// CheckEntry 是一条清单记录与系统事实的并排呈现。
// 没有判定字段：recorded 与 actual 是否算漂移、“记录时就没装”
// 与“装了又删”如何区分，全部留给 LLM 结合上下文决定。
type CheckEntry struct {
	Name            string
	RecordedVersion string
	Installed       bool
	ActualVersion   string
	RecordedSource  string
	ActualSource    string // deriveSource 翻译自探查，未装为 unknown
}

// CheckResult 是 CheckList 的输出。
// Revision 供调用方拿去做后续写操作的 base_revision。
type CheckResult struct {
	Revision int
	Entries  []CheckEntry
}

// CheckList 只读比对清单与系统，不走 Update、不写任何字段。
//
// 软删条目不检查：从清单移除即不再关心系统状态。
// 空名单一次探查都不发起。
// 探查一次批量完成，与 AddPackage 的单查形成对照。
// ActualSource 复用 deriveSource，不另写一份。
func (s *Service) CheckList(ctx context.Context) (CheckResult, error) {
	l, err := s.rec.Load(ctx)
	if err != nil {
		return CheckResult{}, err
	}

	names := make([]string, 0, len(l.Packages))
	for name, p := range l.Packages {
		if p.RemovedAt == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		// 空 Entries 约定为非 nil 空 slice：nil 会被下游信封的
		// omitempty 整个吞掉，字段形态不稳定。
		return CheckResult{Revision: l.Revision, Entries: []CheckEntry{}}, nil
	}

	// 名字来自 map 遍历，顺序被运行时随机化；先排序再探查，
	// 输出顺序确定——理由同 show.go（LLM 噪音 / 客户端缓存 / 测试稳定）。
	slices.Sort(names)

	statuses, err := s.obs.PacmanQuery(ctx, names)
	if err != nil {
		return CheckResult{}, err
	}

	entries := make([]CheckEntry, 0, len(statuses))
	for _, st := range statuses {
		p := l.Packages[st.Name]
		entries = append(entries, CheckEntry{
			Name:            st.Name,
			RecordedVersion: p.Version,
			Installed:       st.Installed,
			ActualVersion:   st.Version,
			RecordedSource:  p.Source,
			ActualSource:    deriveSource(st),
		})
	}
	return CheckResult{Revision: l.Revision, Entries: entries}, nil
}
