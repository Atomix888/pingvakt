package snmp

import (
	"context"
	"reflect"
	"sync"
	"time"
)

// Runner pollar en uppsättning enheter, var och en med sitt eget intervall.
type Runner struct {
	mu      sync.Mutex
	devs    map[string]Device
	cancels map[string]context.CancelFunc
	onPoll  func(Poll)
	ctx     context.Context
}

// NewRunner skapar en Runner som anropar onPoll efter varje avläsning.
func NewRunner(ctx context.Context, onPoll func(Poll)) *Runner {
	return &Runner{devs: map[string]Device{}, cancels: map[string]context.CancelFunc{}, onPoll: onPoll, ctx: ctx}
}

// Set ersätter listan med enheter (oförändrade enheter fortsätter som förut).
func (r *Runner) Set(list []Device) {
	r.mu.Lock()
	defer r.mu.Unlock()
	want := map[string]Device{}
	for _, d := range list {
		d.Normalize()
		if d.Paused || d.Host == "" {
			continue
		}
		want[d.ID] = d
	}
	for id, c := range r.cancels {
		if nd, ok := want[id]; !ok || !reflect.DeepEqual(nd, r.devs[id]) {
			c()
			delete(r.cancels, id)
			delete(r.devs, id)
		}
	}
	for id, d := range want {
		if _, ok := r.cancels[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(r.ctx)
		r.cancels[id], r.devs[id] = cancel, d
		go r.loop(ctx, d)
	}
}

func (r *Runner) loop(ctx context.Context, d Device) {
	// sprid ut första avläsningen lite så att inte alla enheter frågas samtidigt
	select {
	case <-time.After(time.Duration(len(d.ID)%7) * 700 * time.Millisecond):
	case <-ctx.Done():
		return
	}
	tk := time.NewTicker(time.Duration(d.IntervalSec) * time.Second)
	defer tk.Stop()
	var lastNb time.Time
	for {
		pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		nb := time.Since(lastNb) >= 10*time.Minute // grannar ändras sällan – läs dem var tionde minut
		p := poll(pctx, d, nb)
		if nb && p.OK {
			lastNb = time.Now()
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		r.onPoll(p)
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
		}
	}
}
