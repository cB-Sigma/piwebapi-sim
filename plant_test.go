// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package piwebapi_test

import (
	"testing"
	"time"

	"github.com/cB-Sigma/piwebapi"
	"github.com/cB-Sigma/piwebapi-sim/sim"
)

// t0 is the first archived value of the plant; the simulator's clock
// stands an hour later.
var t0 = time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

func at(d time.Duration) string { return piwebapi.Time(t0.Add(d)) }

// plant is a simulated PI System:
//
//   - BLR1.TEMP (Float32, degC): 0, 10 ... 90 a minute apart from t0;
//     the value at 3 min is bad, at 4 min questionable, at 5 min
//     substituted, at 6 min annotated.
//   - BLR1.PUMP (Digital): Running at t0, Stopped at 2 min, the system
//     state Shutdown at 4 min, Running at 6 min.
//   - BLR1.MODE (String): Auto at t0, Manual at 5 min.
//   - AF database Plant: Area1\Boiler1 (template Boiler, category
//     Equipment) with Temperature (BLR1.TEMP, child High Limit = 300),
//     Pump (BLR1.PUMP), Rated (static 250) and Secret (hidden, static);
//     Area1\Boiler2 (template Boiler); Area2 (no template).
//   - Event frames: Trip 1 (2 to 5 min, Boiler1, severity Major,
//     attribute Peak on BLR1.TEMP), Trip 2 (from 8 min, in progress,
//     Boiler2) with child Trip 2a (8 to 9 min).
func plant(t *testing.T) (*sim.PIServer, *piwebapi.Client) {
	t.Helper()
	s := sim.NewPIServer("PISRV01", "demo", "demo-pw")
	t.Cleanup(s.Close)
	s.Now = func() time.Time { return t0.Add(time.Hour) }
	var temp []sim.PIValue
	for i := 0; i < 10; i++ {
		temp = append(temp, sim.PIValue{Time: t0.Add(time.Duration(i) * time.Minute), Value: float64(i * 10)})
	}
	temp[3].Bad = true
	temp[4].Questionable = true
	temp[5].Substituted = true
	temp[6].Annotated = true
	s.AddPoint("BLR1.TEMP", "degC", "boiler 1 temperature", temp...)
	s.AddPoint("BLR1.PUMP", "", "boiler 1 pump", sim.PIValue{Time: t0, State: "Running"}, sim.PIValue{Time: t0.Add(2 * time.Minute), State: "Stopped"},
		sim.PIValue{Time: t0.Add(4 * time.Minute), State: "Shutdown", System: true}, sim.PIValue{Time: t0.Add(6 * time.Minute), State: "Running"})
	s.AddPoint("BLR1.MODE", "", "boiler 1 mode", sim.PIValue{Time: t0, Text: "Auto"}, sim.PIValue{Time: t0.Add(5 * time.Minute), Text: "Manual"})
	s.AddDatabase("Plant", "demo plant")
	must(t, s.AddElement(sim.Element{Path: `Plant\Area1\Boiler1`, Template: "Boiler", Description: "boiler one", Categories: []string{"Equipment"},
		Attributes: []sim.AFAttribute{
			{Name: "Temperature", Point: "BLR1.TEMP", Units: "degC", Categories: []string{"Process"},
				Children: []sim.AFAttribute{{Name: "High Limit", Static: 300.0, Units: "degC"}}},
			{Name: "Pump", Point: "BLR1.PUMP"},
			{Name: "Rated", Static: 250.0, Units: "degC"},
			{Name: "Secret", Static: "x", Hidden: true},
		}}))
	must(t, s.AddElement(sim.Element{Path: `Plant\Area1\Boiler2`, Template: "Boiler"}))
	must(t, s.AddElement(sim.Element{Path: `Plant\Area2`}))
	must(t, s.AddEventFrame(sim.EventFrame{Database: "Plant", Name: "Trip 1", Template: "Trip", Severity: "Major", CanAcknowledge: true,
		Start: t0.Add(2 * time.Minute), End: t0.Add(5 * time.Minute), Elements: []string{`Area1\Boiler1`},
		Attributes: []sim.AFAttribute{{Name: "Peak", Point: "BLR1.TEMP"}}}))
	must(t, s.AddEventFrame(sim.EventFrame{Database: "Plant", Name: "Trip 2", Template: "Trip", Severity: "Minor", Start: t0.Add(8 * time.Minute),
		Elements: []string{`Area1\Boiler2`}}))
	must(t, s.AddEventFrame(sim.EventFrame{Database: "Plant", Name: "Trip 2a", Parent: "Trip 2", Start: t0.Add(8 * time.Minute), End: t0.Add(9 * time.Minute)}))
	return s, piwebapi.New(s.Base(), nil, piwebapi.Basic{User: "demo", Password: "demo-pw"})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func num(t *testing.T, v piwebapi.Value) float64 {
	t.Helper()
	f, ok := v.Float()
	if !ok {
		t.Fatalf("not a number: %s", v.Value)
	}
	return f
}

func nums(t *testing.T, vs []piwebapi.Value) []float64 {
	t.Helper()
	out := make([]float64, len(vs))
	for i, v := range vs {
		f, ok := v.Float()
		if !ok {
			f = -1 // a state
		}
		out[i] = f
	}
	return out
}

func eq(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if d := a[i] - b[i]; d > 1e-6 || d < -1e-6 {
			return false
		}
	}
	return true
}
