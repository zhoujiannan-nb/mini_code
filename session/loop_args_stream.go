package session

import (
	"time"

	"github.com/user/mini_code/provider"
)

const (
	argsFeedBytes = 1024
	argsFeedWait  = 250 * time.Millisecond
)

type argsFeedCall struct {
	name string
	sentArgs string
	last     time.Time
}

type argsStreamer struct {
	emit  func(name, argsSoFar string)
	calls map[int]*argsFeedCall
}

func newArgsStreamer(emit func(name, argsSoFar string)) *argsStreamer {
	if emit == nil {
		return nil
	}
	return &argsStreamer{emit: emit, calls: map[int]*argsFeedCall{}}
}

func (a *argsStreamer) push(calls []provider.StreamToolCallProgress) {
	if a == nil || len(calls) == 0 {
		return
	}
	now := time.Now()
	for _, c := range calls {
		st := a.calls[c.Index]
		if st == nil {
			st = &argsFeedCall{}
			a.calls[c.Index] = st
		}
		if st.name == "" {
			st.name = c.Name
		}
		if st.name == "" || len(c.Args) == 0 || c.Args == st.sentArgs {
			continue
		}
		if grew := len(c.Args) - len(st.sentArgs); grew > 0 && grew < argsFeedBytes && !st.last.IsZero() && now.Sub(st.last) < argsFeedWait {
			continue
		}
		a.emit(st.name, c.Args)
		st.sentArgs = c.Args
		st.last = now
	}
}
