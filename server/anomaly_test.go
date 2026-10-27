package main

import "testing"

func TestAnomalyLimit(t *testing.T) {
	// låg spridning: minsta ökningen styr
	if l := anomalyLimit(hourStat{Mean: 10, Std: 0.5}, 3, 10); l != 20 {
		t.Errorf("%v", l)
	}
	// stor spridning: k·spridning styr
	if l := anomalyLimit(hourStat{Mean: 50, Std: 8}, 3, 10); l != 74 {
		t.Errorf("%v", l)
	}
	// högt medel: minst 25 %
	if l := anomalyLimit(hourStat{Mean: 200, Std: 2}, 3, 10); l != 250 {
		t.Errorf("%v", l)
	}
}
