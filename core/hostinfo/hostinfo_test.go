package hostinfo

import (
	"encoding/json"
	"testing"
)

func TestCollect(t *testing.T) {
	c := New()
	m := c.Collect()
	m = c.Collect()
	b, _ := json.Marshal(m)
	t.Log(string(b))
	if m.MemTotal == 0 || len(m.Disks) == 0 {
		t.Fatal("tomt")
	}
}
