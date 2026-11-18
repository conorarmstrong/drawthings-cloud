package main

import (
	"math"
	"testing"
)

func TestVolPerf(t *testing.T) {
	m := &model{}
	cases := []struct{ iops, tput, wi, wt int }{
		{3000, 500, 3000, 500},
		{100, 50, 3000, 125},
		{99999, 5000, 16000, 1000},
		{3000, 1000, 4000 * 1, 1000}, // 1000 MiB/s needs 4000 IOPS
	}
	for _, c := range cases {
		m.setVolPerf(c.iops, c.tput)
		if m.cfg.ModelVolume.IOPS != c.wi || m.cfg.ModelVolume.ThroughputMiBs != c.wt {
			t.Errorf("set(%d,%d) = %d,%d want %d,%d", c.iops, c.tput, m.cfg.ModelVolume.IOPS, m.cfg.ModelVolume.ThroughputMiBs, c.wi, c.wt)
		}
	}
}

func TestVolCost(t *testing.T) {
	cases := []struct {
		v    ModelVolume
		want float64
	}{
		{ModelVolume{SizeGB: 100, IOPS: 3000, ThroughputMiBs: 500}, 26.68},
		{ModelVolume{SizeGB: 100, IOPS: 3000, ThroughputMiBs: 125}, 9.28},
		{ModelVolume{SizeGB: 100, IOPS: 16000, ThroughputMiBs: 1000}, 125.30},
	}
	for _, c := range cases {
		if got := c.v.monthlyUSD(); math.Abs(got-c.want) > 0.1 {
			t.Errorf("%+v = %.2f want %.2f", c.v, got, c.want)
		}
	}
}
