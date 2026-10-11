// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package negotiate_test logs in with the piwebapi negotiate package
// against the test KDC (sim/kdcsim) and a Kerberos-only simulated PI Web
// API.
package negotiate_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/cB-Sigma/piwebapi"
	"github.com/cB-Sigma/piwebapi-sim/sim"
	"github.com/cB-Sigma/piwebapi-sim/sim/kdcsim"
	"github.com/cB-Sigma/piwebapi/negotiate"
)

// realm is a KDC with user svc and a Kerberos-only PI Web API whose
// service principal the KDC knows.
func realm(t *testing.T) (*kdcsim.KDC, *sim.PIServer) {
	t.Helper()
	kdc, err := kdcsim.Start("PLANT.EXAMPLE")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(kdc.Close)
	if err := kdc.AddUser("svc", "Kerb-pw-1"); err != nil {
		t.Fatal(err)
	}
	pi := sim.NewPIServer("PISRV01", "", "")
	t.Cleanup(pi.Close)
	if pi.Keytab, err = kdc.AddService(negotiate.SPNFor(pi.Base()), "service-pw"); err != nil {
		t.Fatal(err)
	}
	return kdc, pi
}

func TestPasswordAndKeytab(t *testing.T) {
	kdc, pi := realm(t)
	ctx := context.Background()
	k, err := negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "svc", Password: "Kerb-pw-1"})
	if err != nil {
		t.Fatal(err)
	}
	c := piwebapi.New(pi.Base(), nil, k.For(negotiate.SPNFor(pi.Base())))
	if _, err := c.DataServers(ctx); err != nil {
		t.Fatalf("password login: %v", err)
	}
	if got := pi.KerberosLogins(); len(got) != 1 || got[0] != "svc@PLANT.EXAMPLE" {
		t.Errorf("logins %v", got)
	}
	// A write logs in the same way and carries X-Requested-With.
	pi.AddPoint("T", "", "", sim.PIValue{Value: 1})
	pt, _ := c.PointByPath(ctx, `\\PISRV01\T`)
	if err := c.UpdateValue(ctx, pt.WebID, piwebapi.TimedValue{Value: 2.5}, piwebapi.UpdateOptions{}); err != nil {
		t.Errorf("write with Kerberos: %v", err)
	}
	kt, err := kdc.UserKeytab("svc", "Kerb-pw-1")
	if err != nil {
		t.Fatal(err)
	}
	k2, err := negotiate.New(negotiate.Config{Krb5Conf: kdc.Krb5Conf(), Username: "svc", Keytab: base64.StdEncoding.EncodeToString(kt)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := piwebapi.New(pi.Base(), nil, k2.For(negotiate.SPNFor(pi.Base()))).DataServers(ctx); err != nil {
		t.Fatalf("keytab login: %v", err)
	}
	ex := strings.Join(kdc.Exchanges(), "\n")
	if !strings.Contains(ex, "AS svc: preauth required") || !strings.Contains(ex, "TGS svc -> HTTP/127.0.0.1") {
		t.Errorf("exchanges:\n%s", ex)
	}
}

func TestRefusals(t *testing.T) {
	kdc, pi := realm(t)
	ctx := context.Background()
	k, _ := negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "svc", Password: "wrong"})
	_, err := piwebapi.New(pi.Base(), nil, k.For(negotiate.SPNFor(pi.Base()))).DataServers(ctx)
	if !errors.Is(err, negotiate.ErrLogin) {
		t.Errorf("wrong password: %v", err)
	}
	k, _ = negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "nobody", Password: "x"})
	if _, err := piwebapi.New(pi.Base(), nil, k.For(negotiate.SPNFor(pi.Base()))).DataServers(ctx); !errors.Is(err, negotiate.ErrLogin) {
		t.Errorf("unknown user: %v", err)
	}
	k, _ = negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "svc", Password: "Kerb-pw-1"})
	if _, err := piwebapi.New(pi.Base(), nil, k.For("HTTP/unknown.example")).DataServers(ctx); err == nil || errors.Is(err, negotiate.ErrLogin) {
		t.Errorf("unknown service: %v", err)
	}
	blocked := errors.New("blocked")
	k, _ = negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "svc", Password: "Kerb-pw-1",
		CheckKDC: func(string, string, netip.AddrPort) error { return blocked }})
	if _, err := piwebapi.New(pi.Base(), nil, k.For(negotiate.SPNFor(pi.Base()))).DataServers(ctx); !errors.Is(err, blocked) {
		t.Errorf("blocked KDC: %v", err)
	}
}

// When the service's key changes, its tickets no longer decrypt: the
// PI Web API answers 401, the client resets the session, and the next
// request logs in again and is admitted with the new key.
func TestServiceKeyChange(t *testing.T) {
	kdc, pi := realm(t)
	ctx := context.Background()
	k, _ := negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "svc", Password: "Kerb-pw-1"})
	c := piwebapi.New(pi.Base(), nil, k.For(negotiate.SPNFor(pi.Base())))
	if _, err := c.DataServers(ctx); err != nil {
		t.Fatal(err)
	}
	other, err := kdcsim.Start("PLANT.EXAMPLE")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	rotated, err := other.AddService(negotiate.SPNFor(pi.Base()), "rotated-pw")
	if err != nil {
		t.Fatal(err)
	}
	pi.SetKeytab(rotated)
	if _, err := c.DataServers(ctx); !errors.Is(err, piwebapi.ErrAuth) {
		t.Fatalf("old ticket: %v", err)
	}
	current, err := kdc.AddService(negotiate.SPNFor(pi.Base()), "service-pw")
	if err != nil {
		t.Fatal(err)
	}
	pi.SetKeytab(current)
	if _, err := c.DataServers(ctx); err != nil {
		t.Errorf("after reset: %v", err)
	}
	n := 0
	for _, e := range kdc.Exchanges() {
		if strings.HasPrefix(e, "AS svc: ticket-granting ticket") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("logins %d, want 2 (one after the reset):\n%s", n, strings.Join(kdc.Exchanges(), "\n"))
	}
}
