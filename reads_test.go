// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package piwebapi_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/piwebapi"
	"github.com/cB-Sigma/piwebapi-sim/sim"
)

func server(t *testing.T) (*sim.PIServer, time.Time) {
	t.Helper()
	s := sim.NewPIServer("PISRV01", "svc", "pw")
	t.Cleanup(s.Close)
	base := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	var vs []sim.PIValue
	for i := 0; i < 10; i++ {
		vs = append(vs, sim.PIValue{Time: base.Add(time.Duration(i) * time.Minute), Value: float64(i)})
	}
	vs[3].Bad = true
	s.AddPoint("BLR1.TEMP", "degC", "temperature", vs...)
	s.AddPoint("BLR1.PUMP", "", "pump", sim.PIValue{Time: base, State: "Running"},
		sim.PIValue{Time: base.Add(time.Minute), State: "Shutdown", System: true})
	return s, base
}

func TestReads(t *testing.T) {
	s, base := server(t)
	ctx := context.Background()
	c := piwebapi.New(s.Base(), nil, piwebapi.Basic{User: "svc", Password: "pw"})
	ds, err := c.DataServers(ctx)
	if err != nil || len(ds) != 1 || ds[0].Name != "PISRV01" {
		t.Fatalf("data servers %v %v", ds, err)
	}
	pt, err := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	if err != nil || pt.Units != "degC" || pt.WebID == "" {
		t.Fatalf("point %+v %v", pt, err)
	}
	pts, err := c.Points(ctx, ds[0].WebID, "*BLR1*", 0, 10)
	if err != nil || len(pts) != 2 {
		t.Fatalf("points %v %v", pts, err)
	}
	v, err := c.Recorded(ctx, pt.WebID, base, base.Add(time.Hour), 4)
	if err != nil || len(v.Items) != 4 {
		t.Fatalf("recorded %v %v", v, err)
	}
	if x, q := v.Items[3].Decode(); x != 3.0 || q != "bad" {
		t.Errorf("value 3 = %v %q", x, q)
	}
	if x, q := v.Items[2].Decode(); x != 2.0 || q != "" {
		t.Errorf("value 2 = %v %q", x, q)
	}
	iv, err := c.Interpolated(ctx, pt.WebID, base, base.Add(4*time.Minute), 2*time.Minute)
	if err != nil || len(iv.Items) != 3 {
		t.Fatalf("interpolated %v %v", iv, err)
	}
	pump, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.PUMP`)
	pv, _ := c.Recorded(ctx, pump.WebID, base, base.Add(time.Hour), 10)
	if x, q := pv.Items[1].Decode(); x != "Shutdown" || q != "bad,system" {
		t.Errorf("system state = %v %q", x, q)
	}
	_, err = c.PointByPath(ctx, `\\PISRV01\NOPE`)
	var pe *piwebapi.Error
	if !errors.As(err, &pe) || pe.Status != 404 || !strings.Contains(err.Error(), "PI Point not found") {
		t.Errorf("missing point: %v", err)
	}
}

func TestRecordedPages(t *testing.T) {
	s, base := server(t)
	c := piwebapi.New(s.Base(), nil, piwebapi.Basic{User: "svc", Password: "pw"})
	pt, _ := c.PointByPath(context.Background(), `\\PISRV01\BLR1.TEMP`)
	var pages []int
	var last time.Time
	seen := map[time.Time]int{}
	err := c.RecordedPages(context.Background(), pt.WebID, base.Add(-time.Second), base.Add(time.Hour), 4, func(v piwebapi.Values) error {
		pages = append(pages, len(v.Items))
		last = v.Items[len(v.Items)-1].Timestamp
		for _, x := range v.Items {
			seen[x.Timestamp]++
		}
		return nil
	})
	// Each page after the first starts at the last timestamp read and
	// drops the value already passed on: 4, 3, 3.
	if err != nil || len(pages) != 3 || pages[0] != 4 || pages[2] != 3 || len(seen) != 10 || !last.Equal(base.Add(9*time.Minute)) {
		t.Errorf("pages %v last %v err %v", pages, last, err)
	}
	for ts, n := range seen {
		if n != 1 {
			t.Errorf("%v read %d times", ts, n)
		}
	}
	if err := c.RecordedPages(context.Background(), pt.WebID, base, base, 0, nil); err == nil {
		t.Error("page size 0 accepted")
	}
}

type resetter struct {
	piwebapi.Basic
	resets int
}

func (r *resetter) Reset() { r.resets++ }

func TestLogins(t *testing.T) {
	s, _ := server(t)
	s.Token = "tok"
	ctx := context.Background()
	if _, err := piwebapi.New(s.Base(), http.DefaultClient, piwebapi.Bearer("tok")).DataServers(ctx); err != nil {
		t.Errorf("bearer: %v", err)
	}
	r := &resetter{Basic: piwebapi.Basic{User: "svc", Password: "wrong"}}
	_, err := piwebapi.New(s.Base(), nil, r).DataServers(ctx)
	var ae *piwebapi.AuthError
	if !errors.Is(err, piwebapi.ErrAuth) || !errors.As(err, &ae) || ae.Status != 401 || r.resets != 1 {
		t.Errorf("refused login: %v resets %d", err, r.resets)
	}
	if _, err := piwebapi.New(s.Base(), nil, nil).DataServers(ctx); !errors.Is(err, piwebapi.ErrAuth) {
		t.Errorf("no login: %v", err)
	}
}
