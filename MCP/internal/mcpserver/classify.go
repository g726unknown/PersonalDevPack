package mcpserver

import (
	"errors"

	"github.com/g726unknown/pdp/internal/executor"
	"github.com/g726unknown/pdp/internal/recorder"
)

// mapErr 把 executor / recorder 的错误翻译为业务信封。
// 返回 (信封, true) 表示这是业务结果（conflict / rejected）；
// false 表示真实故障，走 error → IsError。
//
// 每层只发明自己领域的新词在这里合流：executor 的 ErrInvalidInput、
// recorder 的 ErrConflict 与 ErrInvalidScope，各自经哨兵识别后
// 穿上信封词汇。
func mapErr(err error) (envelope, bool) {
	switch {
	case errors.Is(err, executor.ErrInvalidInput):
		return envelope{Status: StatusRejected, Summary: err.Error()}, true
	case errors.Is(err, recorder.ErrConflict):
		return envelope{Status: StatusConflict,
			Summary: "base_revision is stale; re-read the list and retry"}, true
	case errors.Is(err, recorder.ErrInvalidScope):
		return envelope{Status: StatusRejected, Summary: err.Error()}, true
	}
	return envelope{}, false
}
