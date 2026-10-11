// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package piwebapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cB-Sigma/piwebapi"
	"github.com/cB-Sigma/piwebapi-sim/sim"
)

func TestObjects(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	if sys, err := c.System(ctx); err != nil || sys.ProductTitle == "" {
		t.Errorf("system %+v %v", sys, err)
	}
	if ui, err := c.UserInfo(ctx); err != nil || ui.Name != "demo" || !ui.IsAuthenticated {
		t.Errorf("userinfo %+v %v", ui, err)
	}
	if v, err := c.Versions(ctx); err != nil || len(v) == 0 {
		t.Errorf("versions %v %v", v, err)
	}
	if st, err := c.Status(ctx); err != nil || st.State != "Running" {
		t.Errorf("status %+v %v", st, err)
	}

	ds, err := c.DataServerByName(ctx, "pisrv01")
	if err != nil || ds.Path != `\\PIServers[PISRV01]` || !ds.IsConnected {
		t.Fatalf("data server by name %+v %v", ds, err)
	}
	if d, err := c.DataServerByPath(ctx, ds.Path); err != nil || d.WebID != ds.WebID {
		t.Errorf("by path %v", err)
	}
	if d, err := c.DataServer(ctx, ds.WebID); err != nil || d.Name != "PISRV01" {
		t.Errorf("by WebId %v", err)
	}
	if _, err := c.DataServerByName(ctx, "OTHER"); !piwebapi.IsNotFound(err) {
		t.Errorf("unknown data server %v", err)
	}
	pt, err := c.Point(ctx, piwebapi.PointWebID(`\\PISRV01\BLR1.TEMP`))
	if err != nil || pt.Name != "BLR1.TEMP" || pt.ID == 0 || pt.PointClass != "classic" {
		t.Errorf("point by a WebId made from its path: %+v %v", pt, err)
	}
	pas, err := c.PointAttributes(ctx, pt.WebID, "eng*")
	if err != nil || len(pas) != 1 || pas[0].Value != "degC" {
		t.Errorf("point attributes %v %v", pas, err)
	}
	found, err := c.PointsByPath(ctx, []string{`\\PISRV01\BLR1.TEMP`, `\\PISRV01\NOPE`})
	if err != nil || len(found) != 2 || found[0].Err != nil || found[0].Object.Name != "BLR1.TEMP" || !piwebapi.IsNotFound(found[1].Err) {
		t.Errorf("points/multiple %+v %v", found, err)
	}

	as, err := c.AssetServers(ctx)
	if err != nil || len(as) != 1 || as[0].Name != "PIAF01" {
		t.Fatalf("asset servers %v %v", as, err)
	}
	if a, err := c.AssetServerByName(ctx, "piaf01"); err != nil || a.WebID != as[0].WebID {
		t.Errorf("asset server by name %v", err)
	}
	if a, err := c.AssetServerByPath(ctx, `\\PIAF01`); err != nil || a.WebID != as[0].WebID {
		t.Errorf("asset server by path %v", err)
	}
	if a, err := c.AssetServer(ctx, as[0].WebID); err != nil || a.Path != `\\PIAF01` {
		t.Errorf("asset server %v", err)
	}
	dbs, err := c.AssetDatabases(ctx, as[0].WebID)
	if err != nil || len(dbs) != 1 || dbs[0].Name != "Plant" {
		t.Fatalf("databases %v %v", dbs, err)
	}
	db, err := c.AssetDatabaseByPath(ctx, `\\PIAF01\Plant`)
	if err != nil || db.WebID != dbs[0].WebID {
		t.Errorf("database by path %v", err)
	}
	if d, err := c.AssetDatabase(ctx, db.WebID); err != nil || d.Description != "demo plant" {
		t.Errorf("database %v", err)
	}

	elNames := func(q piwebapi.ElementQuery) string {
		t.Helper()
		els, err := c.Elements(ctx, db.WebID, q)
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, e := range els {
			n = append(n, e.Name)
		}
		return strings.Join(n, ",")
	}
	for _, tc := range []struct {
		q    piwebapi.ElementQuery
		want string
	}{
		{piwebapi.ElementQuery{}, "Area1,Area2"},
		{piwebapi.ElementQuery{SearchFullHierarchy: true}, "Area1,Area2,Boiler1,Boiler2"},
		{piwebapi.ElementQuery{SearchFullHierarchy: true, TemplateName: "Boiler"}, "Boiler1,Boiler2"},
		{piwebapi.ElementQuery{SearchFullHierarchy: true, NameFilter: "*1"}, "Area1,Boiler1"},
		{piwebapi.ElementQuery{SearchFullHierarchy: true, CategoryName: "Equipment"}, "Boiler1"},
		{piwebapi.ElementQuery{SearchFullHierarchy: true, DescriptionFilter: "boiler*"}, "Boiler1"},
		{piwebapi.ElementQuery{SearchFullHierarchy: true, Paging: piwebapi.Paging{StartIndex: 1, MaxCount: 2}}, "Area2,Boiler1"},
		{piwebapi.ElementQuery{Paging: piwebapi.Paging{SortOrder: "Descending"}}, "Area2,Area1"},
	} {
		if got := elNames(tc.q); got != tc.want {
			t.Errorf("elements %+v = %s, want %s", tc.q, got, tc.want)
		}
	}
	area1, err := c.ElementByPath(ctx, `\\PIAF01\Plant\Area1`)
	if err != nil || !area1.HasChildren {
		t.Fatalf("element by path %+v %v", area1, err)
	}
	kids, err := c.ChildElements(ctx, area1.WebID, piwebapi.ElementQuery{})
	if err != nil || len(kids) != 2 || kids[0].TemplateName != "Boiler" {
		t.Errorf("children %v %v", kids, err)
	}
	b1, err := c.Element(ctx, kids[0].WebID)
	if err != nil || b1.Path != `\\PIAF01\Plant\Area1\Boiler1` || len(b1.CategoryNames) != 1 {
		t.Errorf("element %+v %v", b1, err)
	}
	els, err := c.ElementsByPath(ctx, []string{b1.Path, `\\PIAF01\Plant\Nope`})
	if err != nil || len(els) != 2 || els[0].Err != nil || els[1].Err == nil {
		t.Errorf("elements/multiple %+v %v", els, err)
	}

	attrs, err := c.Attributes(ctx, b1.WebID, piwebapi.AttributeQuery{})
	if err != nil || len(attrs) != 3 {
		t.Fatalf("attributes %v %v", attrs, err)
	}
	tempA := attrs[0]
	if tempA.DataReferencePlugIn != "PI Point" || tempA.ConfigString != `\\PISRV01\BLR1.TEMP` || tempA.Type != "Double" || !tempA.HasChildren || tempA.DefaultUnitsName != "degC" {
		t.Errorf("PI point attribute %+v", tempA)
	}
	if attrs[2].Type != "Double" || !attrs[2].IsConfigurationItem || attrs[1].Type != "EnumerationValue" {
		t.Errorf("static and digital attributes %+v %+v", attrs[2], attrs[1])
	}
	if all, _ := c.Attributes(ctx, b1.WebID, piwebapi.AttributeQuery{ShowHidden: true, SearchFullHierarchy: true}); len(all) != 5 {
		t.Errorf("hidden and children: %d", len(all))
	}
	if typed, _ := c.Attributes(ctx, b1.WebID, piwebapi.AttributeQuery{ValueType: "EnumerationValue"}); len(typed) != 1 || typed[0].Name != "Pump" {
		t.Errorf("value type filter %v", typed)
	}
	kidsA, err := c.ChildAttributes(ctx, tempA.WebID, piwebapi.AttributeQuery{})
	if err != nil || len(kidsA) != 1 || kidsA[0].Path != `\\PIAF01\Plant\Area1\Boiler1|Temperature|High Limit` {
		t.Errorf("child attributes %v %v", kidsA, err)
	}
	if a, err := c.AttributeByPath(ctx, `\\PIAF01\Plant\Area1\Boiler1|Temperature`); err != nil || a.WebID != tempA.WebID {
		t.Errorf("attribute by path %v", err)
	}
	if a, err := c.Attribute(ctx, piwebapi.AttributeWebID(`\\PIAF01\Plant\Area1\Boiler1|Pump`)); err != nil || a.Name != "Pump" {
		t.Errorf("attribute by a WebId made from its path: %v", err)
	}
	ab, err := c.AttributesByPath(ctx, []string{tempA.Path, tempA.Path + "x"})
	if err != nil || len(ab) != 2 || ab[0].Err != nil || ab[1].Err == nil {
		t.Errorf("attributes/multiple %+v %v", ab, err)
	}
	if w, err := piwebapi.ParseWebID(tempA.WebID); err != nil || w.Owner != piwebapi.OwnerElement || !strings.EqualFold(w.Path, tempA.Path) {
		t.Errorf("attribute WebId %+v %v", w, err)
	}
	// Streams read an attribute like its point.
	v, err := c.Value(ctx, tempA.WebID, piwebapi.ValueOptions{})
	if err != nil || num(t, v) != 90 {
		t.Errorf("attribute stream %v %v", v, err)
	}
	if _, err := c.Element(ctx, "P1EmbWlzc2luZw"); !piwebapi.IsNotFound(err) {
		t.Errorf("unknown element %v", err)
	}
}

func efNames(efs []piwebapi.EventFrame) string {
	var n []string
	for _, e := range efs {
		n = append(n, e.Name)
	}
	return strings.Join(n, ",")
}

func TestEventFrames(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	db, _ := c.AssetDatabaseByPath(ctx, `\\PIAF01\Plant`)
	yes := true
	for _, tc := range []struct {
		q    piwebapi.EventFrameQuery
		want string
	}{
		{piwebapi.EventFrameQuery{}, "Trip 1,Trip 2"}, // *-8h to *: both overlap
		{piwebapi.EventFrameQuery{SearchFullHierarchy: true}, "Trip 1,Trip 2,Trip 2a"},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchInclusive, StartTime: at(0), EndTime: at(6 * time.Minute)}, "Trip 1"},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchOverlapped, StartTime: at(6 * time.Minute), EndTime: at(7 * time.Minute)}, ""},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchStartInclusive, StartTime: at(7 * time.Minute), EndTime: at(9 * time.Minute)}, "Trip 2"},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchEndInclusive, StartTime: at(4 * time.Minute), EndTime: at(6 * time.Minute)}, "Trip 1"},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchForwardInProgress, StartTime: at(0)}, "Trip 2"},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchBackwardFromStartTime, StartTime: at(time.Hour)}, "Trip 2,Trip 1"},
		{piwebapi.EventFrameQuery{SearchMode: piwebapi.SearchForwardFromEndTime, StartTime: at(0)}, "Trip 1"},
		{piwebapi.EventFrameQuery{NameFilter: "*2"}, "Trip 2"},
		{piwebapi.EventFrameQuery{Severity: []string{"Major"}}, "Trip 1"},
		{piwebapi.EventFrameQuery{CanBeAcknowledged: &yes}, "Trip 1"},
		{piwebapi.EventFrameQuery{ReferencedElementNameFilter: "Boiler2"}, "Trip 2"},
		{piwebapi.EventFrameQuery{TemplateName: "Trip", Paging: piwebapi.Paging{MaxCount: 1}}, "Trip 1"},
	} {
		efs, err := c.EventFrames(ctx, db.WebID, tc.q)
		if err != nil || efNames(efs) != tc.want {
			t.Errorf("event frames %+v = %q (%v), want %q", tc.q, efNames(efs), err, tc.want)
		}
	}
	if _, err := c.EventFrames(ctx, db.WebID, piwebapi.EventFrameQuery{SearchMode: "Sideways"}); !isStatus(err, 400) {
		t.Errorf("bad search mode %v", err)
	}
	b1 := boiler1(t, c)
	efs, err := c.ElementEventFrames(ctx, b1.WebID, piwebapi.EventFrameQuery{StartTime: at(0), EndTime: "*"})
	if err != nil || efNames(efs) != "Trip 1" || len(efs[0].RefElementWebIDs) != 1 || efs[0].RefElementWebIDs[0] != b1.WebID {
		t.Fatalf("element event frames %v %v", efs, err)
	}
	trip1 := efs[0]
	if trip1.InProgress() || !trip1.StartTime.Equal(t0.Add(2*time.Minute)) || trip1.Severity != "Major" || !trip1.AreValuesCaptured {
		t.Errorf("trip 1 %+v", trip1)
	}
	trip2, err := c.EventFrameByPath(ctx, `\\PIAF01\Plant\EventFrames[Trip 2]`)
	if err != nil || !trip2.InProgress() || !trip2.HasChildren {
		t.Fatalf("trip 2 %+v %v", trip2, err)
	}
	if e, err := c.EventFrame(ctx, trip2.WebID); err != nil || e.Name != "Trip 2" {
		t.Errorf("event frame by WebId %v", err)
	}
	kids, err := c.ChildEventFrames(ctx, trip2.WebID, piwebapi.EventFrameQuery{StartTime: at(0)})
	if err != nil || efNames(kids) != "Trip 2a" || !strings.HasSuffix(kids[0].Path, `EventFrames[Trip 2]\EventFrames[Trip 2a]`) {
		t.Errorf("child event frames %v %v", kids, err)
	}
	ea, err := c.EventFrameAttributes(ctx, trip1.WebID, piwebapi.AttributeQuery{})
	if err != nil || len(ea) != 1 || ea[0].Name != "Peak" {
		t.Fatalf("event frame attributes %v %v", ea, err)
	}
	if w, _ := piwebapi.ParseWebID(ea[0].WebID); w.Owner != piwebapi.OwnerEventFrame {
		t.Errorf("event frame attribute owner %+v", w)
	}
	sm, err := c.Summary(ctx, ea[0].WebID, piwebapi.SummaryOptions{StartTime: piwebapi.Time(trip1.StartTime), EndTime: piwebapi.Time(trip1.EndTime),
		SummaryTypes: []string{"Maximum"}})
	if err != nil || len(sm) != 1 || num(t, sm[0].Value) != 50 {
		t.Errorf("peak during the event frame %v %v", sm, err)
	}
}

func TestBatch(t *testing.T) {
	_, c := plant(t)
	ctx := context.Background()
	post, err := c.BatchPost("streams/{0}/value", nil, piwebapi.TimedValue{Timestamp: at(30 * time.Minute), Value: 42.0}, "point")
	must(t, err)
	post.Parameters = []string{"$.point.Content.WebId"}
	reqs := map[string]piwebapi.BatchRequest{
		"point":   c.BatchGet("points", url.Values{"path": {`\\PISRV01\BLR1.TEMP`}}),
		"value":   {Method: "GET", Resource: c.URL("streams/{0}/value", nil), Parameters: []string{"$.point.Content.WebId"}, ParentIDs: []string{"point"}},
		"write":   post,
		"missing": c.BatchGet("points", url.Values{"path": {`\\PISRV01\NOPE`}}),
		"child":   {Method: "GET", Resource: c.URL("streams/{0}/end", nil), Parameters: []string{"$.missing.Content.WebId"}, ParentIDs: []string{"missing"}},
		"list":    c.BatchGet("dataservers/"+piwebapi.PathWebID(piwebapi.MarkerDataServer, "", `\\PIServers[PISRV01]`)+"/points", url.Values{"nameFilter": {"BLR1.*"}}),
		"each": {Method: "GET", RequestTemplate: &piwebapi.BatchTemplate{Resource: c.URL("streams/{0}/end", nil)},
			Parameters: []string{"$.list.Content.Items[*].WebId"}, ParentIDs: []string{"list"}},
		"set": {Method: "GET", Resource: c.URL("streamsets/value", nil) + "?webId={0}", Parameters: []string{"$.list.Content.Items[*].WebId"}, ParentIDs: []string{"list"}},
	}
	res, err := c.Batch(ctx, reqs)
	if err != nil {
		t.Fatal(err)
	}
	var v piwebapi.Value
	if err := res["value"].Decode(&v); err != nil || num(t, v) != 90 {
		t.Errorf("parameter from a parent: %v %v", v, err)
	}
	if !res["write"].OK() || res["write"].Status != http.StatusAccepted {
		t.Errorf("write in a batch: %+v", res["write"])
	}
	if !piwebapi.IsNotFound(res["missing"].Err()) || res["child"].Status != 409 {
		t.Errorf("failed parent: %d %d", res["missing"].Status, res["child"].Status)
	}
	items, err := res["each"].Items()
	if err != nil || len(items) != 3 {
		t.Fatalf("template: %v %v", items, err)
	}
	var end piwebapi.Value
	if err := items[2].Decode(&end); err != nil || num(t, end) != 42 {
		t.Errorf("template item (BLR1.TEMP after the write): %s %v", end.Value, err)
	}
	var set struct{ Items []piwebapi.StreamValue }
	if err := res["set"].Decode(&set); err != nil || len(set.Items) != 3 {
		t.Errorf("repeated query parameter: %+v %v", set, err)
	}
	// A bad batch as a whole.
	if _, err := c.Batch(ctx, map[string]piwebapi.BatchRequest{"a": {Method: "GET", Resource: c.URL("system", nil), ParentIDs: []string{"b"}},
		"b": {Method: "GET", Resource: c.URL("system", nil), ParentIDs: []string{"a"}}}); !isStatus(err, 400) {
		t.Errorf("cycle: %v", err)
	}
	r2, err := c.Batch(ctx, map[string]piwebapi.BatchRequest{"p": {Method: "GET", Resource: c.URL("system", nil), Parameters: []string{"nope"}}})
	if err != nil || r2["p"].Status != 400 {
		t.Errorf("bad parameter: %+v %v", r2, err)
	}
}

func TestFaults(t *testing.T) {
	s, c := plant(t)
	ctx := context.Background()
	s.AddFault(sim.Fault{Path: "dataservers", Status: 503, RetryAfter: 2, Times: 1})
	_, err := c.DataServers(ctx)
	var e *piwebapi.Error
	if !asErr(err, &e) || e.Status != 503 || !e.Temporary() || e.RetryAfter != 2*time.Second {
		t.Errorf("503: %v", err)
	}
	if _, err := c.DataServers(ctx); err != nil {
		t.Errorf("after the fault: %v", err)
	}
	s.AddFault(sim.Fault{Path: "system", Status: 500, Message: "boom"})
	if _, err := c.System(ctx); !isStatus(err, 500) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("500: %v", err)
	}
	s.AddFault(sim.Fault{Path: "assetservers", Status: 403, Message: "no access to AF"})
	var ae *piwebapi.AuthError
	if _, err := c.AssetServers(ctx); !asErr(err, &ae) || ae.Status != 403 || ae.Message != "no access to AF" {
		t.Errorf("403: %v", err)
	}
	s.AddFault(sim.Fault{Method: "POST", Path: "streams/", Status: 409, Message: "conflict"})
	pt, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	if err := c.UpdateValue(ctx, pt.WebID, piwebapi.TimedValue{Value: 1.0}, piwebapi.UpdateOptions{}); !isStatus(err, 409) {
		t.Errorf("write fault: %v", err)
	}
	if _, err := c.Value(ctx, pt.WebID, piwebapi.ValueOptions{}); err != nil {
		t.Errorf("a POST fault reached a GET: %v", err)
	}
	// Faults also reach a batch's requests.
	res, err := c.Batch(ctx, map[string]piwebapi.BatchRequest{"s": c.BatchGet("system", nil)})
	if err != nil || res["s"].Status != 500 {
		t.Errorf("fault in a batch: %+v %v", res, err)
	}
	s.ClearFaults()
	s.AddFault(sim.Fault{Path: "points", Delay: time.Second})
	tctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := c.PointByPath(tctx, `\\PISRV01\BLR1.TEMP`); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("slow answer: %v", err)
	}
	s.ClearFaults()
	s.SetMaxItems(5)
	if _, err := c.RecordedWith(ctx, pt.WebID, piwebapi.RecordedOptions{MaxCount: 6}); !isStatus(err, 400) {
		t.Errorf("over MaxItems: %v", err)
	}
	if _, err := c.Points(ctx, piwebapi.PathWebID("DS", "", `\\PIServers[PISRV01]`), "*", 0, 6); !isStatus(err, 400) {
		t.Errorf("list over MaxItems: %v", err)
	}
	if err := c.Get(ctx, "nothing/here", nil, nil); !piwebapi.IsNotFound(err) {
		t.Errorf("unknown path: %v", err)
	}
	if _, err := c.Value(ctx, "P1DPbm9uZQ", piwebapi.ValueOptions{}); !piwebapi.IsNotFound(err) {
		t.Errorf("unknown WebId: %v", err)
	}
	if _, err := piwebapi.New(s.Base(), nil, piwebapi.Bearer("wrong")).System(ctx); !errors.Is(err, piwebapi.ErrAuth) {
		t.Errorf("wrong token: %v", err)
	}
}

func TestPaging(t *testing.T) {
	s, c := plant(t)
	ctx := context.Background()
	for i := 0; i < 23; i++ {
		s.AddPoint(fmt.Sprintf("TANK%02d.LEVEL", i), "m", "", sim.PIValue{Time: t0, Value: float64(i)})
	}
	ds, _ := c.DataServerByName(ctx, "PISRV01")
	var pages []int
	seen := map[string]bool{}
	err := c.PointPages(ctx, ds.WebID, "TANK*", 10, func(p []piwebapi.Point) error {
		pages = append(pages, len(p))
		for _, x := range p {
			seen[x.Name] = true
		}
		return nil
	})
	if err != nil || fmt.Sprint(pages) != "[10 10 3]" || len(seen) != 23 {
		t.Errorf("point pages %v %d %v", pages, len(seen), err)
	}
	// Several values at one timestamp across a page boundary are each
	// read once.
	pt, _ := c.PointByPath(ctx, `\\PISRV01\BLR1.TEMP`)
	for i := 0; i < 3; i++ {
		must(t, c.UpdateValue(ctx, pt.WebID, piwebapi.TimedValue{Timestamp: at(2 * time.Minute), Value: float64(200 + i)},
			piwebapi.UpdateOptions{UpdateOption: piwebapi.UpdateInsert}))
	}
	var got []float64
	err = c.RecordedPages(ctx, pt.WebID, t0.Add(-time.Second), t0.Add(time.Hour), 5, func(v piwebapi.Values) error {
		got = append(got, nums(t, v.Items)...)
		return nil
	})
	if err != nil || len(got) != 13 {
		t.Errorf("pages with shared timestamps: %v %v", got, err)
	}
	// A page of values all at one instant moves past it.
	for i := 0; i < 5; i++ {
		must(t, c.UpdateValue(ctx, pt.WebID, piwebapi.TimedValue{Timestamp: at(30 * time.Minute), Value: float64(i)},
			piwebapi.UpdateOptions{UpdateOption: piwebapi.UpdateInsert}))
	}
	got = nil
	err = c.RecordedPages(ctx, pt.WebID, t0.Add(29*time.Minute), t0.Add(time.Hour), 2, func(v piwebapi.Values) error {
		got = append(got, nums(t, v.Items)...)
		return nil
	})
	if err != nil || len(got) < 2 || len(got) > 5 {
		t.Errorf("crowded instant: %v %v", got, err)
	}
}
