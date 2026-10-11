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

func asErr(err error, target any) bool { return errors.As(err, target) }

func boiler1(t *testing.T, c *piwebapi.Client) piwebapi.Element {
	t.Helper()
	el, err := c.ElementByPath(context.Background(), `\\PIAF01\Plant\Area1\Boiler1`)
	must(t, err)
	return el
}

func names(vs []piwebapi.StreamValue) string {
	var n []string
	for _, v := range vs {
		n = append(n, v.Name)
	}
	return strings.Join(n, ",")
}

func TestStreamSets(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	el := boiler1(t, c)
	vs, err := c.StreamSetValue(ctx, piwebapi.Streams{Of: el.WebID}, piwebapi.ValueOptions{})
	if err != nil || names(vs) != "Temperature,Pump,Rated" {
		t.Fatalf("element values %s %v", names(vs), err)
	}
	if num(t, vs[0].Value) != 90 || vs[0].Units != "degC" || num(t, vs[2].Value) != 250 {
		t.Errorf("values %+v", vs)
	}
	if x, _ := vs[1].Value.Decode(); x != "Running" {
		t.Errorf("pump %v", x)
	}
	vs, _ = c.StreamSetValue(ctx, piwebapi.Streams{Of: el.WebID, ShowHidden: true, SearchFullHierarchy: true}, piwebapi.ValueOptions{})
	if names(vs) != "Temperature,High Limit,Pump,Rated,Secret" {
		t.Errorf("hidden and children: %s", names(vs))
	}
	vs, _ = c.StreamSetValue(ctx, piwebapi.Streams{Of: el.WebID, NameFilter: "T*"}, piwebapi.ValueOptions{Time: at(90 * time.Second)})
	if names(vs) != "Temperature" || num(t, vs[0].Value) != 15 {
		t.Errorf("name filter and time: %s", names(vs))
	}
	vs, _ = c.StreamSetValue(ctx, piwebapi.Streams{Of: el.WebID, CategoryName: "Process"}, piwebapi.ValueOptions{})
	if names(vs) != "Temperature" {
		t.Errorf("category: %s", names(vs))
	}
	vs, _ = c.StreamSetEnd(ctx, piwebapi.Streams{Of: el.WebID}, piwebapi.Common{})
	if len(vs) != 3 || num(t, vs[0].Value) != 90 {
		t.Errorf("end %+v", vs)
	}

	recs, err := c.StreamSetRecorded(ctx, piwebapi.Streams{Of: el.WebID}, piwebapi.RecordedOptions{StartTime: at(0), EndTime: at(2 * time.Minute)})
	if err != nil || len(recs) != 3 || len(recs[0].Items) != 3 || len(recs[1].Items) != 2 || recs[0].Units != "degC" {
		t.Fatalf("element recorded %+v %v", recs, err)
	}
	if recs[0].Err() != nil || !isStatus(recs[2].Err(), 400) {
		t.Errorf("static attribute in a recorded set: %v %v", recs[0].Err(), recs[2].Err())
	}

	temp, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	mode, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.MODE`)
	adhoc := piwebapi.Streams{WebIDs: []string{temp.WebID, "P1DPbm9uZQ", mode.WebID}}
	vs, err = c.StreamSetValue(ctx, adhoc, piwebapi.ValueOptions{})
	if err != nil || len(vs) != 3 || vs[0].Err() != nil || !piwebapi.IsNotFound(vs[1].Err()) || vs[2].Name != "BLR1.MODE" {
		t.Fatalf("ad hoc %+v %v", vs, err)
	}
	ad := piwebapi.Streams{WebIDs: []string{temp.WebID, mode.WebID}}
	iv, err := c.StreamSetInterpolated(ctx, ad, piwebapi.InterpolatedOptions{StartTime: at(0), EndTime: at(4 * time.Minute), Interval: "2m"})
	if err != nil || len(iv) != 2 || !eq(nums(t, iv[0].Items), []float64{0, 20, 40}) || len(iv[1].Items) != 3 {
		t.Errorf("ad hoc interpolated %+v %v", iv, err)
	}
	at2, err := c.StreamSetInterpolatedAtTimes(ctx, ad, []string{at(90 * time.Second)}, piwebapi.AtTimesOptions{})
	if err != nil || len(at2) != 2 || num(t, at2[0].Items[0]) != 15 {
		t.Errorf("ad hoc at times %+v %v", at2, err)
	}
	pl, err := c.StreamSetPlot(ctx, ad, piwebapi.PlotOptions{StartTime: at(0), EndTime: at(10 * time.Minute), Intervals: 2})
	if err != nil || len(pl) != 2 || len(pl[0].Items) != 4 {
		t.Errorf("ad hoc plot %+v %v", pl, err)
	}
	sm, err := c.StreamSetSummary(ctx, piwebapi.Streams{WebIDs: []string{temp.WebID}}, piwebapi.SummaryOptions{StartTime: at(0), EndTime: at(9 * time.Minute),
		SummaryTypes: []string{"Average", "Count"}})
	if err != nil || len(sm) != 1 || len(sm[0].Items) != 2 || num(t, sm[0].Items[0].Value) != 45 {
		t.Errorf("ad hoc summary %+v %v", sm, err)
	}
	ra, err := c.StreamSetRecordedAtTime(ctx, ad, at(90*time.Second), piwebapi.RetrieveAtOrBefore, piwebapi.Common{})
	if err != nil || len(ra) != 2 || num(t, ra[0].Value) != 10 {
		t.Errorf("ad hoc recorded at time %+v %v", ra, err)
	}
	rts, err := c.StreamSetRecordedAtTimes(ctx, piwebapi.Streams{Of: el.WebID, NameFilter: "Temp*"}, []string{at(0), at(time.Minute)}, piwebapi.AtTimesOptions{})
	if err != nil || len(rts) != 1 || !eq(nums(t, rts[0].Items), []float64{0, 10}) {
		t.Errorf("element recorded at times %+v %v", rts, err)
	}
	rec2, err := c.StreamSetRecorded(ctx, ad, piwebapi.RecordedOptions{StartTime: at(0), EndTime: at(time.Hour)})
	if err != nil || len(rec2) != 2 || len(rec2[0].Items) != 10 || len(rec2[1].Items) != 2 {
		t.Errorf("ad hoc recorded %+v %v", rec2, err)
	}
	if _, err := c.StreamSetRecorded(ctx, ad, piwebapi.RecordedOptions{StartTime: "not a time"}); !isStatus(err, 400) {
		t.Errorf("bad parameter fails the set: %v", err)
	}
	ef, _ := c.EventFrameByPath(ctx, `\\PIAF01\Plant\EventFrames[Trip 1]`)
	efv, err := c.StreamSetValue(ctx, piwebapi.Streams{Of: ef.WebID}, piwebapi.ValueOptions{Time: at(3 * time.Minute)})
	if err != nil || names(efv) != "Peak" {
		t.Errorf("event frame stream set %s %v", names(efv), err)
	}
	if _, err := c.StreamSetValue(ctx, piwebapi.Streams{Of: temp.WebID}, piwebapi.ValueOptions{}); !piwebapi.IsNotFound(err) {
		t.Errorf("a point is not an element: %v", err)
	}
}

func TestWrites(t *testing.T) {
	s, c := plant(t)
	ctx := context.Background()
	temp, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	id := temp.WebID
	// Replace (the default), at an existing time and at a new one.
	must(t, c.UpdateValue(ctx, id, piwebapi.TimedValue{Timestamp: at(time.Minute), Value: 11.0}, piwebapi.UpdateOptions{}))
	must(t, c.UpdateValue(ctx, id, piwebapi.TimedValue{Timestamp: at(30 * time.Second), Value: 5.5}, piwebapi.UpdateOptions{BufferOption: piwebapi.BufferDoNotBuffer}))
	v, _ := c.RecordedAtTime(ctx, id, at(time.Minute), piwebapi.RetrieveExact, piwebapi.Common{})
	if num(t, v) != 11 || len(s.Values("BLR1.TEMP")) != 11 {
		t.Errorf("replace: %s, %d values", v.Value, len(s.Values("BLR1.TEMP")))
	}
	err := c.UpdateValue(ctx, id, piwebapi.TimedValue{Timestamp: at(time.Minute), Value: 12.0}, piwebapi.UpdateOptions{UpdateOption: piwebapi.UpdateNoReplace})
	if !isStatus(err, 409) {
		t.Errorf("no replace: %v", err)
	}
	if err := c.UpdateValue(ctx, id, piwebapi.TimedValue{Timestamp: at(7 * time.Hour), Value: 1.0}, piwebapi.UpdateOptions{UpdateOption: piwebapi.UpdateReplaceOnly}); !isStatus(err, 409) {
		t.Errorf("replace only: %v", err)
	}
	must(t, c.UpdateValue(ctx, id, piwebapi.TimedValue{Timestamp: at(30 * time.Second), Value: 0}, piwebapi.UpdateOptions{UpdateOption: piwebapi.UpdateRemove}))
	if len(s.Values("BLR1.TEMP")) != 10 {
		t.Error("remove")
	}
	must(t, c.UpdateValue(ctx, id, piwebapi.TimedValue{Timestamp: at(time.Minute), Value: 13.0}, piwebapi.UpdateOptions{UpdateOption: piwebapi.UpdateInsert}))
	if len(s.Values("BLR1.TEMP")) != 11 {
		t.Error("insert keeps both values")
	}
	// Now (the server's clock) when no timestamp is given; a bad flag.
	bad := false
	must(t, c.UpdateValue(ctx, id, piwebapi.TimedValue{Value: 99.5, Good: &bad}, piwebapi.UpdateOptions{}))
	end, _ := c.End(ctx, id, piwebapi.Common{})
	if _, q := end.Decode(); num(t, end) != 99.5 || q != "bad" || !end.Timestamp.Equal(t0.Add(time.Hour)) {
		t.Errorf("end after write %v %q", end, q)
	}
	if err := c.UpdateValue(ctx, id, piwebapi.TimedValue{Value: "text"}, piwebapi.UpdateOptions{}); !isStatus(err, 400) {
		t.Errorf("text to a float point: %v", err)
	}

	// Several values, one of them refused: 207.
	err = c.UpdateValues(ctx, id, []piwebapi.TimedValue{{Timestamp: at(20 * time.Minute), Value: 1.0}, {Timestamp: at(21 * time.Minute), Value: "x"},
		{Timestamp: at(22 * time.Minute), Value: 3.0}}, piwebapi.UpdateOptions{})
	var pe *piwebapi.PartialError
	if !asErr(err, &pe) || len(pe.Items) != 1 || pe.Items[0].Index != 1 || pe.Items[0].Status != 400 {
		t.Errorf("partial values: %v", err)
	}
	must(t, c.UpdateValues(ctx, id, []piwebapi.TimedValue{{Timestamp: at(23 * time.Minute), Value: 4.0}}, piwebapi.UpdateOptions{}))

	// Digital states by name, by code, and a system state; text.
	pump, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.PUMP`)
	must(t, c.UpdateValue(ctx, pump.WebID, piwebapi.TimedValue{Timestamp: at(30 * time.Minute), Value: piwebapi.DigitalState{Name: "Stopped"}}, piwebapi.UpdateOptions{}))
	must(t, c.UpdateValue(ctx, pump.WebID, piwebapi.TimedValue{Timestamp: at(31 * time.Minute), Value: 0}, piwebapi.UpdateOptions{}))
	must(t, c.UpdateValue(ctx, pump.WebID, piwebapi.TimedValue{Timestamp: at(32 * time.Minute), Value: piwebapi.DigitalState{Name: "I/O Timeout", IsSystem: true}}, piwebapi.UpdateOptions{}))
	if err := c.UpdateValue(ctx, pump.WebID, piwebapi.TimedValue{Value: "Exploded"}, piwebapi.UpdateOptions{}); !isStatus(err, 400) {
		t.Errorf("unknown state: %v", err)
	}
	pv, _ := c.RecordedWith(ctx, pump.WebID, piwebapi.RecordedOptions{StartTime: at(30 * time.Minute), EndTime: at(32 * time.Minute)})
	var got []string
	for _, x := range pv.Items {
		n, q := x.Decode()
		got = append(got, n.(string)+"/"+q)
	}
	if strings.Join(got, " ") != "Stopped/ Running/ I/O Timeout/bad,system" {
		t.Errorf("digital writes %v", got)
	}
	mode, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.MODE`)
	must(t, c.UpdateValue(ctx, mode.WebID, piwebapi.TimedValue{Value: "Remote"}, piwebapi.UpdateOptions{}))

	// A read-only point refuses with 403.
	s.SetReadOnly("BLR1.TEMP", true)
	if err := c.UpdateValue(ctx, id, piwebapi.TimedValue{Value: 1.0}, piwebapi.UpdateOptions{}); !errors.Is(err, piwebapi.ErrAuth) {
		t.Errorf("read-only point: %v", err)
	}
	s.SetReadOnly("BLR1.TEMP", false)

	// Stream sets: one value per stream, values per stream.
	err = c.StreamSetUpdateValue(ctx, []piwebapi.StreamUpdate{{WebID: id, Value: piwebapi.TimedValue{Timestamp: at(40 * time.Minute), Value: 1.0}},
		{WebID: "P1DPbm9uZQ", Value: piwebapi.TimedValue{Value: 1.0}}}, piwebapi.UpdateOptions{})
	if !asErr(err, &pe) || len(pe.Items) != 1 || pe.Items[0].Index != 1 || pe.Items[0].Status != 404 || pe.Items[0].WebID != "P1DPbm9uZQ" {
		t.Errorf("stream set value: %v", err)
	}
	err = c.StreamSetUpdateValues(ctx, []piwebapi.StreamValuesUpdate{
		{WebID: id, Items: []piwebapi.TimedValue{{Timestamp: at(41 * time.Minute), Value: 1.0}, {Timestamp: at(42 * time.Minute), Value: "x"}}},
		{WebID: mode.WebID, Items: []piwebapi.TimedValue{{Timestamp: at(41 * time.Minute), Value: "Local"}}}}, piwebapi.UpdateOptions{})
	if !asErr(err, &pe) || len(pe.Items) != 1 || pe.Items[0].Index != 0 || pe.Items[0].Sub != 1 {
		t.Errorf("stream set values: %v", err)
	}
	must(t, c.StreamSetUpdateValue(ctx, []piwebapi.StreamUpdate{{WebID: id, Value: piwebapi.TimedValue{Timestamp: at(43 * time.Minute), Value: 2.0}}}, piwebapi.UpdateOptions{}))

	// Attributes: through streams (to the point) and static values.
	el := boiler1(t, c)
	attrs, _ := c.Attributes(ctx, el.WebID, piwebapi.AttributeQuery{})
	byName := map[string]piwebapi.Attribute{}
	for _, a := range attrs {
		byName[a.Name] = a
	}
	must(t, c.UpdateValue(ctx, byName["Temperature"].WebID, piwebapi.TimedValue{Timestamp: at(50 * time.Minute), Value: 77.0}, piwebapi.UpdateOptions{}))
	if v, _ := c.RecordedAtTime(ctx, id, at(50*time.Minute), piwebapi.RetrieveExact, piwebapi.Common{}); num(t, v) != 77 {
		t.Errorf("attribute write did not reach the point: %s", v.Value)
	}
	must(t, c.SetAttributeValue(ctx, byName["Rated"].WebID, piwebapi.TimedValue{Value: 260.0}))
	if v, err := c.AttributeValue(ctx, byName["Rated"].WebID); err != nil || num(t, v) != 260 {
		t.Errorf("static value %v %v", v, err)
	}
	// The point's snapshot: the value written at the clock's now above.
	if v, err := c.AttributeValue(ctx, byName["Temperature"].WebID); err != nil || num(t, v) != 99.5 {
		t.Errorf("attribute value through its point %s %v", v.Value, err)
	}
	if err := c.SetAttributeValue(ctx, byName["Temperature"].WebID, piwebapi.TimedValue{Value: 1.0}); !isStatus(err, 400) {
		t.Errorf("static write to a PI point attribute: %v", err)
	}
}

// Requests that change data need X-Requested-With and a JSON body.
func TestCSRFDefence(t *testing.T) {
	s, c := plant(t)
	temp, _ := c.PointByPath(context.Background(), `\\PISRV01\BLR1.TEMP`)
	post := func(header, ctype string) int {
		req, _ := http.NewRequest(http.MethodPost, s.Base()+"/streams/"+temp.WebID+"/value", strings.NewReader(`{"Value":1}`))
		req.SetBasicAuth("demo", "demo-pw")
		if header != "" {
			req.Header.Set("X-Requested-With", header)
		}
		req.Header.Set("Content-Type", ctype)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := post("", "application/json"); got != 403 {
		t.Errorf("without X-Requested-With: %d", got)
	}
	if got := post("x", "text/plain"); got != 415 {
		t.Errorf("text body: %d", got)
	}
	if got := post("x", "application/json"); got != 202 {
		t.Errorf("with the header: %d", got)
	}
	s.SetCSRFDefence(false)
	if got := post("", "application/json"); got != 202 {
		t.Errorf("defence off: %d", got)
	}
}

func TestStreamUpdates(t *testing.T) {
	s, c := plant(t)
	ctx := context.Background()
	temp, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	reg, err := c.RegisterUpdates(ctx, temp.WebID)
	if err != nil || reg.Marker == "" || reg.Source != temp.WebID {
		t.Fatalf("register %+v %v", reg, err)
	}
	s.Append("BLR1.TEMP", sim.PIValue{Time: t0.Add(10 * time.Minute), Value: 100})
	must(t, c.UpdateValue(ctx, temp.WebID, piwebapi.TimedValue{Timestamp: at(time.Minute), Value: 11.0}, piwebapi.UpdateOptions{}))
	u, err := c.Updates(ctx, reg.Marker, piwebapi.Common{})
	if err != nil || len(u.Events) != 2 || u.Events[0].Action != "Add" || u.Events[1].Action != "Update" || u.Marker == reg.Marker {
		t.Fatalf("updates %+v %v", u, err)
	}
	again, _ := c.Updates(ctx, u.Marker, piwebapi.Common{})
	if len(again.Events) != 0 {
		t.Errorf("no change since: %+v", again.Events)
	}
	if _, err := c.Updates(ctx, "not-a-marker", piwebapi.Common{}); !isStatus(err, 400) {
		t.Errorf("bad marker: %v", err)
	}
	mode, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.MODE`)
	regs, err := c.RegisterStreamSetUpdates(ctx, []string{temp.WebID, mode.WebID, "P1DPbm9uZQ"})
	if err != nil || len(regs) != 3 || regs[0].Err() != nil || regs[2].Err() == nil {
		t.Fatalf("stream set register %+v %v", regs, err)
	}
	must(t, c.UpdateValue(ctx, mode.WebID, piwebapi.TimedValue{Value: "Local"}, piwebapi.UpdateOptions{}))
	sets, err := c.StreamSetUpdates(ctx, []string{regs[0].Marker, regs[1].Marker}, piwebapi.Common{})
	if err != nil || len(sets) != 2 || len(sets[0].Events) != 0 || len(sets[1].Events) != 1 {
		t.Errorf("stream set updates %+v %v", sets, err)
	}
}
