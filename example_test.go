// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package piwebapi_test

import (
	"context"
	"fmt"
	"time"

	"github.com/cB-Sigma/piwebapi"
	"github.com/cB-Sigma/piwebapi-sim/sim"
)

// Read a point's recorded values, write one, and read an element's
// attributes in one stream set call, against the simulator.
func Example() {
	s := sim.NewPIServer("PISRV01", "demo", "demo-pw")
	defer s.Close()
	base := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	s.AddPoint("BLR1.STEAM.TEMP", "degC", "steam temperature",
		sim.PIValue{Time: base, Value: 181.5}, sim.PIValue{Time: base.Add(time.Minute), Value: 182})
	s.AddDatabase("Plant", "")
	_ = s.AddElement(sim.Element{Path: `Plant\Boiler1`, Attributes: []sim.AFAttribute{
		{Name: "Steam Temperature", Point: "BLR1.STEAM.TEMP"}, {Name: "Rated", Static: 190.0, Units: "degC"}}})

	ctx := context.Background()
	c := piwebapi.New(s.Base(), nil, piwebapi.Basic{User: "demo", Password: "demo-pw"})
	pt, err := c.PointByPath(ctx, `\\PISRV01\BLR1.STEAM.TEMP`)
	if err != nil {
		fmt.Println(err)
		return
	}
	_ = c.UpdateValue(ctx, pt.WebID, piwebapi.TimedValue{Timestamp: piwebapi.Time(base.Add(2 * time.Minute)), Value: 183.25}, piwebapi.UpdateOptions{})
	vals, err := c.Recorded(ctx, pt.WebID, base, base.Add(time.Hour), 1000)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, v := range vals.Items {
		x, quality := v.Decode()
		fmt.Println(v.Timestamp.Format(time.TimeOnly), x, pt.Units, quality == "")
	}
	el, _ := c.ElementByPath(ctx, `\\PIAF01\Plant\Boiler1`)
	set, _ := c.StreamSetValue(ctx, piwebapi.Streams{Of: el.WebID}, piwebapi.ValueOptions{Time: piwebapi.Time(base.Add(time.Minute))})
	for _, v := range set {
		x, _ := v.Value.Decode()
		fmt.Println(v.Name, x)
	}
	// Output:
	// 08:00:00 181.5 degC true
	// 08:01:00 182 degC true
	// 08:02:00 183.25 degC true
	// Steam Temperature 182
	// Rated 190
}
