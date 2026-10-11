// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cB-Sigma/piwebapi"
	"github.com/cB-Sigma/piwebapi-sim/sim"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestRunner(t *testing.T) {
	for _, args := range [][]string{{"-token", "t"}, {"-realm", "DEMO.EXAMPLE"}} {
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan *sim.PIServer, 1)
		var out syncBuf
		done := make(chan error, 1)
		go func() { done <- run(ctx, args, &out, func(s *sim.PIServer) { got <- s }) }()
		var s *sim.PIServer
		select {
		case s = <-got:
		case err := <-done:
			t.Fatal(err)
		case <-time.After(5 * time.Second):
			t.Fatal("not ready")
		}
		c := piwebapi.New(s.Base(), nil, piwebapi.Basic{User: "demo", Password: "demo-pw"})
		pt, err := c.PointByPath(ctx, `\\PISRV01\BLR1.STEAM.TEMP`)
		if err != nil {
			t.Fatal(err)
		}
		v, err := c.Recorded(ctx, pt.WebID, time.Now().Add(-2*time.Hour), time.Now(), 1000)
		if err != nil || len(v.Items) != 61 {
			t.Errorf("recorded %d %v", len(v.Items), err)
		}
		el, err := c.ElementByPath(ctx, `\\PIAF01\Demo\Boiler1`)
		if err != nil {
			t.Fatal(err)
		}
		if vs, err := c.StreamSetValue(ctx, piwebapi.Streams{Of: el.WebID}, piwebapi.ValueOptions{}); err != nil || len(vs) != 4 {
			t.Errorf("Boiler1 values %d %v", len(vs), err)
		}
		if efs, err := c.ElementEventFrames(ctx, el.WebID, piwebapi.EventFrameQuery{}); err != nil || len(efs) != 1 || !efs[0].InProgress() {
			t.Errorf("event frames %v %v", efs, err)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "PI Web API at") || (args[0] == "-realm") != strings.Contains(out.String(), "krb5.conf") {
			t.Errorf("output = %s", out.String())
		}
	}
}

func TestRunnerFlags(t *testing.T) {
	for _, args := range [][]string{{"-addr", "nowhere"}, {"-bogus"}, {"extra"}} {
		if err := run(context.Background(), args, &bytes.Buffer{}, nil); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
