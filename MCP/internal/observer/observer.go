package observer

import "context"

// Execer 是命令执行的抽象：生产用 OSExecer（os/exec），测试用假实现。
//
// Run 把 stderr 一并交出，是因为退出码语义不够用：
// pacman -Q 遇到有包未找到时退出码是 1——这个 1 不是故障，
// 必须靠 stderr 内容区分“部分未安装”与真实错误，
// 二值 err 装不下这个信息。
type Execer interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// Observer 持有一个 Execer，所有查询经由它执行命令。
type Observer struct{ exec Execer }

// New 返回使用 ex 执行命令的 Observer。
func New(ex Execer) *Observer {
	return &Observer{exec: ex}
}
