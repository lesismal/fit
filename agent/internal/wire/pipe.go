package wire

import (
	"errors"
	"sync"
)

// pipe delivers messages in order to a session on its own goroutine, like a connection's read loop.
// pipe 在独立协程中按序把消息交给会话，模拟连接的读循环。
type pipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	q      [][]byte
	closed bool
}

func newPipe(dst func() *Session, onClose func()) *pipe {
	p := &pipe{}
	p.cond = sync.NewCond(&p.mu)
	go func() {
		for {
			p.mu.Lock()
			for len(p.q) == 0 && !p.closed {
				p.cond.Wait()
			}
			if p.closed {
				p.mu.Unlock()
				onClose()
				return
			}
			msg := p.q[0]
			p.q[0] = nil
			p.q = p.q[1:]
			p.mu.Unlock()
			_ = dst().Receive(msg)
		}
	}()
	return p
}

func (p *pipe) send(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("wire: pipe closed")
	}
	p.q = append(p.q, append([]byte(nil), b...))
	p.cond.Signal()
	return nil
}

func (p *pipe) close() {
	p.mu.Lock()
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
}

// Pipe connects a dialer and a listener session in memory, with the same semantics (flow control, cancel, idle
// timeout) as over a connection. dialer and listener are the Handlers of each side; closeFn closes both sessions.
// A process uses it to run its own stream methods locally, e.g. for its web console.
//
// Pipe 在内存中连接拨号方与监听方会话，语义（流控、取消、空闲超时）与经连接时相同。dialer、listener 为两端各自的
// Handlers；closeFn 关闭两端会话。进程用它在本地执行自己的流方法，例如供其 Web 控制台使用。
func Pipe(opts Options, dialer, listener Handlers) (d, l *Session, closeFn func()) {
	toL := newPipe(func() *Session { return l }, func() { l.Close(nil) })
	toD := newPipe(func() *Session { return d }, func() { d.Close(nil) })
	do, lo := opts, opts
	do.Dialer, do.Handlers = true, dialer
	lo.Dialer, lo.Handlers = false, listener
	d = NewSession(do, toL.send)
	l = NewSession(lo, toD.send)
	return d, l, func() { toL.close(); toD.close() }
}
