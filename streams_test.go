// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package piwebapi_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/cB-Sigma/piwebapi"
)

func TestStreamReads(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	temp, err := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	must(t, err)
	id := temp.WebID

	v, err := c.Value(ctx, id, piwebapi.ValueOptions{})
	if err != nil || num(t, v) != 90 || v.Units != "degC" {
		t.Errorf("snapshot %v %v", v, err)
	}
	v, _ = c.Value(ctx, id, piwebapi.ValueOptions{Time: at(90 * time.Second)})
	if num(t, v) != 15 {
		t.Errorf("value at 1.5 min = %s", v.Value)
	}
	v, _ = c.Value(ctx, id, piwebapi.ValueOptions{Time: at(210 * time.Second)})
	if _, q := v.Decode(); q != "bad" {
		t.Errorf("after a bad value: %q", q)
	}
	v, _ = c.Value(ctx, id, piwebapi.ValueOptions{Time: at(-time.Hour)})
	if st, ok := v.State(); !ok || st.Name != "No Data" || !st.IsSystem {
		t.Errorf("before the first value: %s", v.Value)
	}
	v, _ = c.End(ctx, id, piwebapi.Common{})
	if num(t, v) != 90 || !v.Timestamp.Equal(t0.Add(9*time.Minute)) {
		t.Errorf("end %v", v)
	}

	rec := func(o piwebapi.RecordedOptions) []float64 {
		t.Helper()
		vs, err := c.RecordedWith(ctx, id, o)
		if err != nil {
			t.Fatalf("recorded %+v: %v", o, err)
		}
		return nums(t, vs.Items)
	}
	if got := rec(piwebapi.RecordedOptions{StartTime: at(time.Minute), EndTime: at(3 * time.Minute)}); !eq(got, []float64{10, 20, 30}) {
		t.Errorf("inside %v", got)
	}
	if got := rec(piwebapi.RecordedOptions{StartTime: at(90 * time.Second), EndTime: at(150 * time.Second), BoundaryType: piwebapi.BoundaryOutside}); !eq(got, []float64{10, 20, 30}) {
		t.Errorf("outside %v", got)
	}
	if got := rec(piwebapi.RecordedOptions{StartTime: at(30 * time.Second), EndTime: at(90 * time.Second), BoundaryType: piwebapi.BoundaryInterpolated}); !eq(got, []float64{5, 10, 15}) {
		t.Errorf("interpolated boundaries %v", got)
	}
	if got := rec(piwebapi.RecordedOptions{StartTime: at(0), EndTime: at(time.Hour), FilterExpression: "'BLR1.TEMP' > 50"}); !eq(got, []float64{60, 70, 80, 90}) {
		t.Errorf("filtered %v", got)
	}
	vs, _ := c.RecordedWith(ctx, id, piwebapi.RecordedOptions{StartTime: at(0), EndTime: at(time.Hour), FilterExpression: "'.' > 50", IncludeFilteredValues: true})
	if st, ok := vs.Items[0].State(); len(vs.Items) != 10 || !ok || st.Name != "Filtered" {
		t.Errorf("filtered values included: %d %v", len(vs.Items), vs.Items[0])
	}
	if got := rec(piwebapi.RecordedOptions{StartTime: at(9 * time.Minute), EndTime: at(7 * time.Minute)}); !eq(got, []float64{90, 80, 70}) {
		t.Errorf("backwards %v", got)
	}
	if got := rec(piwebapi.RecordedOptions{StartTime: "*-2h", EndTime: "*", MaxCount: 2}); !eq(got, []float64{0, 10}) {
		t.Errorf("relative times and maxCount %v", got)
	}
	vs, _ = c.RecordedWith(ctx, id, piwebapi.RecordedOptions{StartTime: at(3 * time.Minute), EndTime: at(6 * time.Minute)})
	quality := []string{}
	for _, x := range vs.Items {
		_, q := x.Decode()
		quality = append(quality, q)
	}
	if len(quality) != 4 || quality[0] != "bad" || quality[1] != "questionable" || quality[2] != "substituted" || quality[3] != "" || !vs.Items[3].Annotated {
		t.Errorf("qualities %q", quality)
	}
	if _, err := c.RecordedWith(ctx, id, piwebapi.RecordedOptions{FilterExpression: "BadVal('.')"}); !isStatus(err, 400) {
		t.Errorf("unsupported filter: %v", err)
	}

	iv, err := c.InterpolatedWith(ctx, id, piwebapi.InterpolatedOptions{StartTime: at(0), EndTime: at(4 * time.Minute), Interval: "2m"})
	if got := nums(t, iv.Items); err != nil || !eq(got, []float64{0, 20, 40}) {
		t.Errorf("interpolated %v %v", got, err)
	}
	iv, _ = c.InterpolatedWith(ctx, id, piwebapi.InterpolatedOptions{StartTime: at(0), EndTime: at(4 * time.Minute), Interval: "2m", SyncTime: at(time.Minute)})
	if got := nums(t, iv.Items); !eq(got, []float64{10, 30}) {
		t.Errorf("sync time %v", got)
	}
	iv, _ = c.InterpolatedAtTimes(ctx, id, []string{at(90 * time.Second), at(-time.Hour)}, piwebapi.AtTimesOptions{})
	if len(iv.Items) != 2 || num(t, iv.Items[1]) != 15 {
		t.Errorf("at times %v", iv.Items)
	}
	if st, ok := iv.Items[0].State(); !ok || st.Name != "No Data" {
		t.Errorf("at times before data: %v", iv.Items[0])
	}
	iv, _ = c.InterpolatedAtTimes(ctx, id, []string{at(90 * time.Second), at(150 * time.Second)}, piwebapi.AtTimesOptions{SortOrder: "Descending"})
	if got := nums(t, iv.Items); !eq(got, []float64{25, 15}) {
		t.Errorf("descending at times %v", got)
	}
	pl, _ := c.Plot(ctx, id, piwebapi.PlotOptions{StartTime: at(0), EndTime: at(10 * time.Minute), Intervals: 2})
	if got := nums(t, pl.Items); !eq(got, []float64{0, 40, 50, 90}) {
		t.Errorf("plot %v", got)
	}

	for _, tc := range []struct {
		time, mode string
		want       float64
	}{
		{at(90 * time.Second), piwebapi.RetrieveAtOrBefore, 10},
		{at(90 * time.Second), "", 10},
		{at(90 * time.Second), piwebapi.RetrieveAfter, 20},
		{at(2 * time.Minute), piwebapi.RetrieveExact, 20},
		{at(2 * time.Minute), piwebapi.RetrieveBefore, 10},
		{at(2 * time.Minute), piwebapi.RetrieveAtOrAfter, 20},
	} {
		v, err := c.RecordedAtTime(ctx, id, tc.time, tc.mode, piwebapi.Common{})
		if err != nil || num(t, v) != tc.want {
			t.Errorf("recorded at %s %s: %s %v", tc.time, tc.mode, v.Value, err)
		}
	}
	v, _ = c.RecordedAtTime(ctx, id, at(90*time.Second), piwebapi.RetrieveExact, piwebapi.Common{})
	if st, ok := v.State(); !ok || st.Name != "No Data" {
		t.Errorf("exact miss: %s", v.Value)
	}
	ra, _ := c.RecordedAtTimes(ctx, id, []string{at(2 * time.Minute), at(30 * time.Second)}, piwebapi.AtTimesOptions{RetrievalMode: piwebapi.RetrieveAtOrBefore})
	if got := nums(t, ra.Items); !eq(got, []float64{0, 20}) {
		t.Errorf("recorded at times %v", got)
	}
	if _, err := c.RecordedAtTime(ctx, id, "*", "Sideways", piwebapi.Common{}); !isStatus(err, 400) {
		t.Errorf("bad retrieval mode: %v", err)
	}
}

func TestSummaries(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	pt, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	sums, err := c.Summary(ctx, pt.WebID, piwebapi.SummaryOptions{StartTime: at(0), EndTime: at(9 * time.Minute), SummaryTypes: []string{piwebapi.SummaryAll}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"Total": 45 * 540.0 / 86400, "Average": 45, "Minimum": 0, "Maximum": 90, "Range": 90, "Count": 9, "PercentGood": 90}
	got := map[string]float64{}
	for _, s := range sums {
		got[s.Type] = num(t, s.Value)
	}
	for k, w := range want {
		if math.Abs(got[k]-w) > 1e-6 {
			t.Errorf("%s = %v, want %v", k, got[k], w)
		}
	}
	if got["StdDev"] <= got["PopulationStdDev"] || got["PopulationStdDev"] <= 0 {
		t.Errorf("std devs %v %v", got["StdDev"], got["PopulationStdDev"])
	}
	for _, s := range sums {
		if s.Type == "Maximum" && !s.Value.Timestamp.Equal(t0.Add(9*time.Minute)) {
			t.Errorf("maximum's time %v", s.Value.Timestamp)
		}
	}
	ew, _ := c.Summary(ctx, pt.WebID, piwebapi.SummaryOptions{StartTime: at(0), EndTime: at(9 * time.Minute), SummaryTypes: []string{"Average"},
		CalculationBasis: piwebapi.CalculationEventWeighted})
	if len(ew) != 1 || math.Abs(num(t, ew[0].Value)-420.0/9) > 1e-6 {
		t.Errorf("event-weighted average %v", ew)
	}
	per, _ := c.Summary(ctx, pt.WebID, piwebapi.SummaryOptions{StartTime: at(0), EndTime: at(9 * time.Minute), SummaryTypes: []string{"Count"}, SummaryDuration: "3m"})
	if len(per) != 3 || num(t, per[0].Value) != 3 || num(t, per[2].Value) != 4 {
		t.Errorf("per interval %v", per)
	}
	if _, err := c.Summary(ctx, pt.WebID, piwebapi.SummaryOptions{SummaryTypes: []string{"Median"}}); !isStatus(err, 400) {
		t.Errorf("unknown summary type: %v", err)
	}
	def, err := c.Summary(ctx, pt.WebID, piwebapi.SummaryOptions{StartTime: at(0), EndTime: at(9 * time.Minute)})
	if err != nil || len(def) != 1 || def[0].Type != "Total" {
		t.Errorf("default summary %v %v", def, err)
	}
}

// Digital and string points, system states.
func TestOtherPointTypes(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	pump, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.PUMP`)
	if pump.Type != "Digital" || !pump.Step || pump.DigitalSetName == "" {
		t.Errorf("pump %+v", pump)
	}
	vs, _ := c.RecordedWith(ctx, pump.WebID, piwebapi.RecordedOptions{StartTime: at(0), EndTime: at(time.Hour)})
	var names, quals []string
	for _, v := range vs.Items {
		x, q := v.Decode()
		names = append(names, x.(string))
		quals = append(quals, q)
	}
	if len(names) != 4 || names[1] != "Stopped" || names[2] != "Shutdown" || quals[2] != "bad,system" || quals[3] != "" {
		t.Errorf("pump %v %v", names, quals)
	}
	iv, _ := c.InterpolatedWith(ctx, pump.WebID, piwebapi.InterpolatedOptions{StartTime: at(time.Minute), EndTime: at(3 * time.Minute), Interval: "1m"})
	if x, _ := iv.Items[0].Decode(); len(iv.Items) != 3 || x != "Running" {
		t.Errorf("digital interpolation steps: %v", iv.Items)
	}
	mode, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.MODE`)
	v, _ := c.Value(ctx, mode.WebID, piwebapi.ValueOptions{Time: at(7 * time.Minute)})
	if x, _ := v.Decode(); mode.Type != "String" || x != "Manual" {
		t.Errorf("string point %v %v", mode.Type, x)
	}
}

func isStatus(err error, status int) bool {
	var e *piwebapi.Error
	return asErr(err, &e) && e.Status == status
}
