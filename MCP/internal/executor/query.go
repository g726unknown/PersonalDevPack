package executor

import (
	"context"
	"fmt"
	"slices"

	"github.com/g726unknown/pdp/internal/observer"
)

// PkgEntry 是 QueryPackages 的输出条目：探查事实 + 翻译后的 source。
type PkgEntry struct {
	Name      string
	Installed bool
	Version   string
	Foreign   bool
	Source    string // 翻译自 Foreign，见 deriveSource
}

// QueryPackages 只查系统，不读写清单。
//
// 空 names 透传 observer 语义：返回空、不跑命令；
// 名单含空串 → ErrInvalidInput（拒绝的口径归 executor，
// 而不是透传 observer 的笼统错误）。
// entries 覆盖全部请求名；missing 是未安装名字的便利清单，
// 信息不重复。
func (s *Service) QueryPackages(ctx context.Context, names []string) (entries []PkgEntry, missing []string, err error) {
	if slices.Contains(names, "") {
		return nil, nil, fmt.Errorf("%w: empty package name in input", ErrInvalidInput)
	}

	statuses, err := s.obs.PacmanQuery(ctx, names)
	if err != nil {
		return nil, nil, err
	}

	entries = make([]PkgEntry, 0, len(statuses))
	for _, st := range statuses {
		entries = append(entries, PkgEntry{
			Name:      st.Name,
			Installed: st.Installed,
			Version:   st.Version,
			Foreign:   st.Foreign,
			Source:    deriveSource(st),
		})
		if !st.Installed {
			missing = append(missing, st.Name)
		}
	}
	return entries, missing, nil
}

// deriveSource 把探查事实翻译为 source。
// observer 只报布尔事实（Foreign），这层翻译属于 executor：
// 将来换 apt 时 observer 换实现、翻译换地方，互不拖累。
// QueryPackages 与 AddPackage 共用（source 覆盖为空时后者也走它）。
func deriveSource(st observer.PkgStatus) string {
	switch {
	case !st.Installed:
		return SourceUnknown
	case st.Foreign:
		return SourceAUR
	default:
		return SourcePacman
	}
}
