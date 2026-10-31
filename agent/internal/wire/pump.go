package wire

import "sync"

// Pump forwards src's data frames to dst as they arrive, without a goroutine
// of its own: a frame goes on at once while dst's peer has granted credit,
// and waits otherwise, held against src's window, so that a slow reader
// behind dst holds back the writer behind src and nothing piles up between
// them. What waits is bounded by src's window and one frame.
//
// end runs once: with io.EOF when src ended and every frame went on, or with
// why src or dst failed; frames is how many frames src delivered. Pump does
// not end or cancel either stream: end decides. first, when not nil, runs
// before the first frame goes on.
func Pump(src, dst *Stream, first func(), end func(err error, frames int)) {
	p := &pump{src: src, dst: dst, first: first, end: end}
	src.OnData(p.frame)
}

type pump struct {
	src, dst *Stream
	first    func()
	end      func(error, int)

	mu      sync.Mutex
	queue   [][]byte
	frames  int
	srcEnd  error // src ended with this, once the queue has gone
	ended   bool
	sending bool
}

func (p *pump) frame(data []byte, err error) {
	p.mu.Lock()
	if p.ended {
		p.mu.Unlock()
		if data != nil {
			p.src.Release(len(data))
		}
		return
	}
	if err != nil {
		p.srcEnd = err
	} else {
		p.frames++
		if p.frames == 1 && p.first != nil {
			f := p.first
			p.mu.Unlock()
			f()
			p.mu.Lock()
		}
		p.queue = append(p.queue, data)
	}
	p.mu.Unlock()
	p.drain()
}

// drain sends what waits while dst has credit.
func (p *pump) drain() {
	p.mu.Lock()
	if p.sending || p.ended {
		p.mu.Unlock()
		return
	}
	p.sending = true
	for {
		if len(p.queue) == 0 {
			p.sending = false
			if p.srcEnd != nil {
				p.finishLocked(p.srcEnd)
				return
			}
			p.mu.Unlock()
			return
		}
		data := p.queue[0]
		p.mu.Unlock()
		ok, err := p.dst.TrySend(data)
		p.mu.Lock()
		switch {
		case err != nil:
			p.sending = false
			p.finishLocked(err)
			return
		case ok:
			p.queue[0] = nil
			p.queue = p.queue[1:]
			p.mu.Unlock()
			p.src.Release(len(data))
			p.mu.Lock()
		default:
			// No credit: wait for it, unless it came meanwhile.
			p.sending = false
			p.mu.Unlock()
			if !p.dst.OnCredit(p.drain) {
				p.drain()
			}
			return
		}
	}
}

// finishLocked ends the pump; p.mu is held, and released.
func (p *pump) finishLocked(err error) {
	if p.ended {
		p.mu.Unlock()
		return
	}
	p.ended = true
	n := 0
	for _, q := range p.queue {
		n += len(q)
	}
	p.queue = nil
	frames := p.frames
	p.mu.Unlock()
	if n > 0 {
		p.src.Release(n)
	}
	p.end(err, frames)
}
