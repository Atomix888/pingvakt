package main

import (
	"testing"
	"time"
)

func TestRoamGap(t *testing.T) {
	at := time.Unix(1000, 0)
	var pts []pingPt
	for i := -20; i <= 10; i++ {
		ok := !(i >= -4 && i <= -2) // tre tappade svar (500 ms isär) strax före bytet
		pts = append(pts, pingPt{at.Add(time.Duration(i) * 500 * time.Millisecond), ok})
	}
	if ms := roamGapMs(pts, at); ms != 2000 {
		t.Errorf("avbrott %d ms, ville ha 2000", ms)
	}
	for i := range pts {
		pts[i].ok = true
	}
	if ms := roamGapMs(pts, at); ms != -1 {
		t.Errorf("inget avbrott: %d", ms)
	}
}
