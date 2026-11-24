// Package executor 编排用例：何时探查、口径如何裁定、错误如何归类。
//
// 分层：mcpserver（MCP 外壳）→ executor（本包，流程与口径）→
// observer（只读事实）+ pathobserver（只读路径证据）+ recorder
// （清单数据的唯一权威）。字段级规则在 recorder（Upsert /
// MarkRemoved / SetScope 纯方法）；本包只定顺序与口径，
// 不替调用方改主意。
//
// 文件分工的规矩：一个用例一个文件，文件名 = 用例动词
// （query / add / remove / check / show / inspect / scope）；
// 本文件只住三样——服务装配（Service / New）、跨用例共享的
// 包级词表（哨兵、source 枚举）、不构成用例的透传
// （SystemInfo / Inventory）。新用例进场不进本文件。
package executor

import (
	"context"
	"errors"

	"github.com/g726unknown/pdp/internal/observer"
	"github.com/g726unknown/pdp/internal/pathobserver"
	"github.com/g726unknown/pdp/internal/recorder"
)

// ErrInvalidInput 表示调用参数不合法（空名、非法 type、名单含空串），
// mcpserver 映射为信封 status=rejected。
//
// conflict 与 invalid scope 不需要本包哨兵：recorder 的
// ConflictError / ErrInvalidScope 原样透传，mcpserver 用 errors.Is
// 判别——每层只发明自己领域的新词，不重复别人的。
var ErrInvalidInput = errors.New("invalid input")

// source 值域（DESIGN.md 第 4 节）：pacman / aur / manual / unknown。
// 跨用例共享的词表（query 翻译、add 覆盖），所以住装配文件。
const (
	SourcePacman  = "pacman"
	SourceAUR     = "aur"
	SourceManual  = "manual"
	SourceUnknown = "unknown"
)

// Service 组合观察者、记录者与路径观察者，实现用例层。
//
// 依赖具体类型而非接口：observer / recorder / pathobserver 各自已有
// 成熟的替身（stubExec / 临时目录 / 临时 home），executor 测试直接
// 复用它们，等于免费获得三层真逻辑的集成测试；再造一层接口假货，
// 业务规则的测试就退化成“两个手写假货互相对表”——断言的全是
// 自己写下的预期，不是系统行为。
// Go 惯例：接口定义在消费侧、且只在有第二个实现时才定义——
// 三个依赖当前都只有一个实现。
type Service struct {
	obs  *observer.Observer
	rec  *recorder.Recorder
	pobs *pathobserver.Prober
}

// New 装配 executor：观察者递系统事实，路径观察者递文件系统证据，
// 记录者落盘。
func New(obs *observer.Observer, rec *recorder.Recorder, pobs *pathobserver.Prober) *Service {
	return &Service{obs: obs, rec: rec, pobs: pobs}
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
