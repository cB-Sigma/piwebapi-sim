// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Command piwebapisim serves a simulated AVEVA PI Web API with a few demo
// points holding the last hour of values, an AF database (Demo) with an
// element and an event frame, and with -realm also a Kerberos KDC so
// that clients can log in with HTTP Negotiate.
//
//	go run ./cmd/piwebapisim -addr 127.0.0.1:8443 -user demo -password demo-pw
//	go run ./cmd/piwebapisim -realm DEMO.EXAMPLE -user demo -password demo-pw
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/cB-Sigma/piwebapi-sim/sim"
	"github.com/cB-Sigma/piwebapi-sim/sim/kdcsim"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

// run serves until ctx ends; ready, when set, gets the server once it
// listens.
func run(ctx context.Context, args []string, out io.Writer, ready func(*sim.PIServer)) error {
	fs := flag.NewFlagSet("piwebapisim", flag.ContinueOnError)
	fs.SetOutput(out)
	addr := fs.String("addr", "127.0.0.1:0", "listen address")
	archive := fs.String("archive", "PISRV01", "Data Archive name")
	user := fs.String("user", "demo", "basic (and, with -realm, Kerberos) user")
	password := fs.String("password", "demo-pw", "the user's password")
	token := fs.String("token", "", "bearer token also accepted")
	realm := fs.String("realm", "", "start a KDC for this realm and accept Kerberos")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %v", fs.Args())
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	s := sim.NewUnstartedPIServer(*archive, *user, *password)
	s.Listener.Close()
	s.Listener = ln
	s.Token = *token
	if *realm != "" {
		kdc, err := kdcsim.Start(*realm)
		if err != nil {
			ln.Close()
			return err
		}
		defer kdc.Close()
		if err := kdc.AddUser(*user, *password); err != nil {
			ln.Close()
			return err
		}
		host, _, _ := net.SplitHostPort(ln.Addr().String())
		if s.Keytab, err = kdc.AddService("HTTP/"+host, "demo-service-pw"); err != nil {
			ln.Close()
			return err
		}
		fmt.Fprintf(out, "KDC for %s on %s (tcp and udp); service principal HTTP/%s\nkrb5.conf:\n%s\n", *realm, kdc.Addr, host, kdc.Krb5Conf())
	}
	demo(s, time.Now().UTC())
	s.Start()
	defer s.Close()
	fmt.Fprintf(out, "PI Web API at %s (Data Archive %s; user %s)\n", s.Base(), *archive, *user)
	if ready != nil {
		ready(s)
	}
	<-ctx.Done()
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

// demo adds three made-up points with a value a minute for the last
// hour, an AF database Demo with element Boiler1 whose attributes refer
// to them, and an event frame for the pump's stop.
func demo(s *sim.PIServer, now time.Time) {
	base := now.Add(-time.Hour).Truncate(time.Minute)
	var temp, flow []sim.PIValue
	for i := 0; i <= 60; i++ {
		t := base.Add(time.Duration(i) * time.Minute)
		temp = append(temp, sim.PIValue{Time: t, Value: 180 + 5*math.Sin(float64(i)/10)})
		flow = append(flow, sim.PIValue{Time: t, Value: float64(40 + i%7)})
	}
	s.AddPoint("BLR1.STEAM.TEMP", "degC", "Boiler 1 steam temperature", temp...)
	s.AddPoint("BLR1.FEED.FLOW", "m3/h", "Boiler 1 feed water flow", flow...)
	s.AddPoint("BLR1.PUMP.STATE", "", "Boiler 1 feed pump", sim.PIValue{Time: base, State: "Running"},
		sim.PIValue{Time: base.Add(30 * time.Minute), State: "Stopped"})
	s.AddDatabase("Demo", "demo plant")
	_ = s.AddElement(sim.Element{Path: `Demo\Boiler1`, Template: "Boiler", Description: "Boiler 1",
		Attributes: []sim.AFAttribute{{Name: "Steam Temperature", Point: "BLR1.STEAM.TEMP", Units: "degC"},
			{Name: "Feed Flow", Point: "BLR1.FEED.FLOW", Units: "m3/h"}, {Name: "Pump", Point: "BLR1.PUMP.STATE"},
			{Name: "Rated Temperature", Static: 190.0, Units: "degC"}}})
	_ = s.AddEventFrame(sim.EventFrame{Database: "Demo", Name: "Pump stopped", Template: "Stop", Severity: "Minor",
		Start: base.Add(30 * time.Minute), Elements: []string{"Boiler1"}})
}
