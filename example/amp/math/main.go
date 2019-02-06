package main

import (
	"context"
	"fmt"
	"time"

	"github.com/mnu5/svckit/amp"
	"github.com/mnu5/svckit/amp/broker"
	"github.com/mnu5/svckit/amp/nsq"
	"github.com/mnu5/svckit/env"
	"github.com/mnu5/svckit/health"
	"github.com/mnu5/svckit/httpi"
	"github.com/mnu5/svckit/signal"
)

const (
	v1Topic  = "math.v1"
	reqTopic = "math.req"

	methodAdd    = "add"
	methodReplay = "replay"
)

type params struct {
	X int64 `json:"x,omitempty"`
	Y int64 `json:"y,omitempty"`
}

type rsp struct {
	Z int64 `json:"z"`
}

type msg struct {
	params *params
	isFull bool
}

func updateType(m *msg) uint8 {
	if m.isFull {
		return amp.Full
	}
	return amp.Diff
}

func producer(ctx context.Context) chan *msg {
	out := make(chan *msg, 1)

	go func() {
		defer close(out)
		// init
		i := int64(1)
		x := i
		y := x
		// define publish function
		publish := func() {
			p := &params{
				X: x,
				Y: y,
			}
			out <- &msg{
				params: p,
				isFull: y != 0,
			}
		}
		publish()
		// loop and generate full/diffs
		diff := time.Tick(time.Second)
		full := time.Tick(30 * time.Second)
		for {
			select {
			case <-diff:
				i++
				x = i
				y = 0
				publish()
			case <-full:
				x = i
				y = x
				publish()
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}

func msg2ampMsg(in <-chan *msg) <-chan *amp.Msg {
	out := make(chan *amp.Msg, 1)
	go func() {
		defer close(out)
		for m := range in {
			out <- amp.NewPublish(v1Topic, "i", amp.TS(), updateType(m), m.params)
		}
	}()
	return out
}

func debugHTTP() {
	health.Set(func() (health.Status, []byte) {
		return health.Passing, []byte("OK")
	})
	httpi.Start(env.Address(""))
}

type requests struct {
	broker *broker.ReplayBroker
}

func (r *requests) handler(m *amp.Msg) (*amp.Msg, error) {
	switch m.Path() {
	case methodAdd:
		p := &params{}
		if err := m.Unmarshal(p); err != nil {
			return nil, err
		}
		z := p.X + p.Y
		return m.Response(amp.JSONMarshaler(&rsp{Z: z})), nil
	case methodReplay:
		r.broker.Replay("")
	default:
		return nil, fmt.Errorf("unknown method")
	}
	return nil, nil
}

func main() {
	interupt := signal.InteruptContext()

	broker := broker.NewWithReplay()
	responder := nsq.NewResponder(interupt, (&requests{broker: broker}).handler, reqTopic)
	defer responder.Wait()

	pub := nsq.NewPublisher(broker.Pipe(msg2ampMsg(producer(interupt))))
	defer pub.Wait()

	debugHTTP()
}
