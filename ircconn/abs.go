package ircconn

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/hlandau/ircproto/ircparse"
)

// Abstract interface for an IRC message sink.
type Sink interface {
	WriteMsg(ctx context.Context, msg *ircparse.Msg) error
}

// Abstract interface for an IRC message source.
type Source interface {
	ReadMsg(ctx context.Context) (*ircparse.Msg, error)
}

// Abstract interface for an IRC message source/sink.
type Abstract interface {
	Close()
	Sink
	Source
}

type prependSource struct {
	underlying Source
	msgs       []*ircparse.Msg
	mutex      sync.Mutex
	doneMsgs   uint32
}

// Given an underlying Source, returns an Source that yields the specified list
// of messages first. After all the specified messages have been returned, all
// future calls to ReadMsg call the underlying abstract.
func PrependMsgs(underlying Source, msgs []*ircparse.Msg) Source {
	return &prependSource{
		underlying: underlying,
		msgs:       msgs,
	}
}

func (p *prependSource) ReadMsg(ctx context.Context) (*ircparse.Msg, error) {
	if atomic.LoadUint32(&p.doneMsgs) != 0 {
		return p.underlying.ReadMsg(ctx)
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()
	if len(p.msgs) == 0 {
		p.msgs = nil
		atomic.StoreUint32(&p.doneMsgs, 1)
		return p.underlying.ReadMsg(ctx)
	}

	msg := p.msgs[0]
	p.msgs = p.msgs[1:]
	return msg, nil
}
