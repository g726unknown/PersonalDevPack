// Package executor 编排用例：何时探查、口径如何裁定、错误如何归类。
//
// 分层：mcpserver（MCP 外壳）→ executor（本包，流程与口径）→
// observer（只读事实）+ recorder（清单数据的唯一权威）。
// 字段级规则在 recorder（Upsert / MarkRemoved）；本包只定顺序与口径，
// 不替调用方改主意。
package executor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/recorder"
)

// ErrInvalidInput 表示调用参数不合法（空名、非法 type、名单含空串），
// mcpserver 映射为信封 status=rejected。
//
// conflict 不需要本包哨兵：recorder.ConflictError 原样透传，
// mcpserver 用 errors.Is(err, recorder.ErrConflict) 判别——
// 每层只发明自己领域的新词，不重复别人的。
var ErrInvalidInput = errors.New("invalid input")

// source 值域（DESIGN.md 第 4 节）：pacman / aur / manual / unknown。
const (
	SourcePacman  = "pacman"
	SourceAUR     = "aur"
	SourceManual  = "manual"
	SourceUnknown = "unknown"
)

// Service 组合观察者与记录者，实现用例层。
//
// 依赖具体类型而非接口：observer / recorder 各自已有成熟的替身
// （stubExec / 临时目录），executor 测试直接复用它们，等于免费获得
// 三层真逻辑的集成测试；再造一层接口假货，业务规则的测试就退化成
// “两个手写假货互相对表”——断言的全是自己写下的预期，不是系统行为。
// Go 惯例：接口定义在消费侧、且只在有第二个实现时才定义——
// 两个依赖当前都只有一个实现。
type Service struct {
	obs *observer.Observer
	rec *recorder.Recorder
}

// New 装配 executor：观察者递事实，记录者落盘。
func New(obs *observer.Observer, rec *recorder.Recorder) *Service {
	return &Service{obs: obs, rec: rec}
}

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

// SystemInfo 透传 observer 的系统事实。
// handler 用 := 接住即可，mcpserver 不需要 import observer——
// Go 只有显式写出类型名才需要 import，字段访问不需要。
// 这是“外壳只认识执行者”在类型系统上的兑现方式。
func (s *Service) SystemInfo(ctx context.Context) (observer.SystemInfo, error) {
	return s.obs.SystemInfo(ctx)
}

// Inventory 透传 recorder 的全量清单。
// 资源 pdp://state/inventory 要全量（含软删条目）；
// ShowList 故意丢掉软删，所以这里走 Load——
// 这条差异是那份资源与 pdp_list_show 并存的理由。
func (s *Service) Inventory(ctx context.Context) (*recorder.List, error) {
	return s.rec.Load(ctx)
}

// AddInput 是 AddPackage 的调用参数。
type AddInput struct {
	Name         string
	Type         string
	Source       string // 非空时覆盖自动判断
	BaseRevision int
}

// AddResult 是 AddPackage 的输出。
// Package 是值拷贝——不把 Upsert 返回的内部指针递给调用方：
// 调用方拿到的是快照，清单后续变化不影响它。
type AddResult struct {
	Action   recorder.Action
	Revision int
	Package  recorder.Package
	Warning  string // 未安装时非空
}

// AddPackage 探查系统 → upsert 清单，四步固定：
//
//	口径先行 → 探查 → source 裁定 → Update 落盘
//
// 校验放在探查之前：拒绝的调用不花一次子进程。recorder 仍是最终
// 执法者（Upsert 在锁内还会再验一次 type），而提前校验与最终校验
// 引用同一个函数（recorder.ValidType），镜像不存在，漂移不可能发生。
// unchanged 照常落盘、revision 照常 +1——recorder 的既定语义，本包不做特判。
// v1 假定目标环境有 pacman；无 pacman 机器的行为见 DESIGN 待定。
func (s *Service) AddPackage(ctx context.Context, in AddInput) (AddResult, error) {
	if in.Name == "" {
		return AddResult{}, fmt.Errorf("%w: empty package name", ErrInvalidInput)
	}
	if !recorder.ValidType(in.Type) {
		return AddResult{}, fmt.Errorf("%w: invalid type %q", ErrInvalidInput, in.Type)
	}

	statuses, err := s.obs.PacmanQuery(ctx, []string{in.Name})
	if err != nil {
		return AddResult{}, err
	}
	st := statuses[0]

	source := in.Source // 非空时覆盖自动判断
	if source == "" {
		source = deriveSource(st)
	}

	warning := ""
	if !st.Installed {
		warning = "not installed on this system; version is recorded as empty"
	}

	var act recorder.Action
	var pkg *recorder.Package
	newList, err := s.rec.Update(ctx, in.BaseRevision, func(l *recorder.List) error {
		// 闭包捕获 act 与 pkg：fn 只在这里能见到清单，
		// Upsert 的返回值只能经捕获变量带出去——
		// Go 写 RMW 回调的标准姿势。fn 的错误原样传出，
		// Update 不包裹（recorder 既定契约）。
		var uerr error
		act, pkg, uerr = l.Upsert(in.Name,
			recorder.Facts{Type: in.Type, Version: st.Version, Source: source}, time.Now())
		return uerr
	})
	if err != nil {
		// ConflictError 与 Upsert 的校验错误都从这里透传。
		return AddResult{}, err
	}
	return AddResult{
		Action:   act,
		Revision: newList.Revision,
		Package:  *pkg,
		Warning:  warning,
	}, nil
}

// RemoveInput 是 RemovePackage 的调用参数。
type RemoveInput struct {
	Name         string
	BaseRevision int
}

// RemoveResult 是 RemovePackage 的输出。
type RemoveResult struct {
	Action   recorder.Action // removed / already_removed / not_present
	Revision int
}

// RemovePackage 从清单软删除一个条目。
//
// 它是唯一不需要探查的写用例：从清单移除与系统无关——
// 包在机器上装没装，与“用户不再想跟踪它”这个意图无关。
// not_present 不是 error：action 原样返回，
// 由 mcpserver 映射为信封 status=not_found。
func (s *Service) RemovePackage(ctx context.Context, in RemoveInput) (RemoveResult, error) {
	if in.Name == "" {
		return RemoveResult{}, fmt.Errorf("%w: empty package name", ErrInvalidInput)
	}

	var act recorder.Action
	newList, err := s.rec.Update(ctx, in.BaseRevision, func(l *recorder.List) error {
		act = l.MarkRemoved(in.Name, time.Now())
		return nil
	})
	if err != nil {
		return RemoveResult{}, err
	}
	return RemoveResult{Action: act, Revision: newList.Revision}, nil
}

// deriveSource 把探查事实翻译为 source。
// observer 只报布尔事实（Foreign），这层翻译属于 executor：
// 将来换 apt 时 observer 换实现、翻译换地方，互不拖累。
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
