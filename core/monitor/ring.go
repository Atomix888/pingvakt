package monitor

// ring är en cirkulär buffert med mätpunkter i tidsordning.
type ring struct {
	buf   []Sample
	start int
	n     int
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]Sample, capacity)}
}

func (r *ring) push(s Sample) {
	c := len(r.buf)
	if r.n < c {
		r.buf[(r.start+r.n)%c] = s
		r.n++
		return
	}
	r.buf[r.start] = s
	r.start = (r.start + 1) % c
}

func (r *ring) at(i int) Sample { return r.buf[(r.start+i)%len(r.buf)] }

// last returnerar de k senaste i kronologisk ordning.
func (r *ring) last(k int) []Sample {
	if k > r.n {
		k = r.n
	}
	out := make([]Sample, k)
	for i := 0; i < k; i++ {
		out[i] = r.at(r.n - k + i)
	}
	return out
}

// since returnerar alla mätpunkter med T > t.
func (r *ring) since(t int64) []Sample {
	// binärsökning – punkterna ligger i tidsordning
	lo, hi := 0, r.n
	for lo < hi {
		mid := (lo + hi) / 2
		if r.at(mid).T > t {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	out := make([]Sample, r.n-lo)
	for i := lo; i < r.n; i++ {
		out[i-lo] = r.at(i)
	}
	return out
}

func (r *ring) resize(capacity int) {
	if capacity == len(r.buf) || capacity < 1 {
		return
	}
	keep := r.last(capacity)
	r.buf = make([]Sample, capacity)
	r.start, r.n = 0, 0
	for _, s := range keep {
		r.push(s)
	}
}
