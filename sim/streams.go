// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// src is a stream: a PI point, or an AF attribute that refers to one or
// holds a static value.
type src struct {
	webID, name, path, units string
	pt                       *piPoint
	attr                     *afAttr
}

func (s *PIServer) pointSrc(p *piPoint) *src {
	return &src{webID: s.pointWebID(p), name: p.name, path: s.pointPath(p), units: p.units, pt: p}
}

func (s *PIServer) attrSrc(a *afAttr) *src {
	sr := &src{webID: a.webID(), name: a.name, path: a.path(), units: a.units, attr: a}
	if a.point != "" {
		sr.pt = s.points[a.point]
		if sr.units == "" && sr.pt != nil {
			sr.units = sr.pt.units
		}
	}
	return sr
}

// source finds the stream of a point or attribute WebId.
func (s *PIServer) source(id string) *src {
	if p := s.pointByWebID(id); p != nil {
		return s.pointSrc(p)
	}
	if a := s.attrByWebID(id); a != nil {
		return s.attrSrc(a)
	}
	return nil
}

type readErr struct {
	code int
	msg  string
}

func (s *PIServer) valueJSON(sr *src, v PIValue) map[string]any {
	var val any = v.Value
	switch {
	case v.State != "":
		code := 0
		if sr.pt != nil {
			code = sr.pt.code(v)
		}
		if v.System {
			code = systemCode(v.State)
		}
		val = map[string]any{"Name": v.State, "Value": code, "IsSystem": v.System}
	case v.Text != "":
		val = v.Text
	case math.IsNaN(v.Value) || math.IsInf(v.Value, 0):
		val = map[string]any{"Name": "Calc Failed", "Value": systemCode("Calc Failed"), "IsSystem": true}
	}
	return map[string]any{"Timestamp": v.Time.UTC().Format(time.RFC3339Nano), "Value": val, "UnitsAbbreviation": sr.units,
		"Good": !v.Bad && !v.System, "Questionable": v.Questionable, "Substituted": v.Substituted, "Annotated": v.Annotated}
}

// systemCode gives each system state a stable code of its own (not the
// PI System's numbering).
func systemCode(name string) int {
	h := 0
	for _, r := range strings.ToLower(name) {
		h = (h*31 + int(r)) % 1000
	}
	return 1000 + h
}

func noData(t time.Time) PIValue { return PIValue{Time: t, State: "No Data", System: true} }

func numeric(v PIValue) bool { return v.State == "" && v.Text == "" }

// at is the value of a stream at t: interpolated between the archived
// values around it for a Float point, else the value at or before t.
// ok is false before the first value or after the last one.
func at(vs []PIValue, t time.Time, step bool) (PIValue, bool) {
	for i := len(vs) - 1; i >= 0; i-- {
		a := vs[i]
		if a.Time.After(t) {
			continue
		}
		if a.Time.Equal(t) {
			return a, true
		}
		if i+1 >= len(vs) {
			return PIValue{}, false
		}
		b := vs[i+1]
		out := a
		out.Time = t
		out.Annotated = false
		if step || !numeric(a) || !numeric(b) || a.Bad || a.System {
			return out, true
		}
		span := b.Time.Sub(a.Time).Seconds()
		f := t.Sub(a.Time).Seconds() / span
		out.Value = math.Round((a.Value+(b.Value-a.Value)*f)*1e6) / 1e6
		out.Bad = a.Bad || b.Bad
		out.Questionable = a.Questionable || b.Questionable
		out.Substituted = a.Substituted || b.Substituted
		return out, true
	}
	return PIValue{}, false
}

// filter is a filter expression the simulator reads: 'name' op number,
// where op is >, >=, <, <=, = or <> ('.' is the stream itself).
type filter struct {
	op string
	n  float64
}

var filterRe = regexp.MustCompile(`^'[^']*'\s*(>=|<=|<>|=|>|<)\s*(-?[0-9.]+)$`)

func parseFilter(expr string) (*filter, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, true
	}
	m := filterRe.FindStringSubmatch(expr)
	if m == nil {
		return nil, false
	}
	n, err := strconv.ParseFloat(m[2], 64)
	if err != nil {
		return nil, false
	}
	return &filter{op: m[1], n: n}, true
}

func (f *filter) keep(v PIValue) bool {
	if f == nil {
		return true
	}
	if !numeric(v) || v.Bad {
		return false
	}
	switch f.op {
	case ">":
		return v.Value > f.n
	case ">=":
		return v.Value >= f.n
	case "<":
		return v.Value < f.n
	case "<=":
		return v.Value <= f.n
	case "=":
		return v.Value == f.n
	}
	return v.Value != f.n
}

func filtered(t time.Time) PIValue { return PIValue{Time: t, State: "Filtered", System: true} }

// read answers one stream read; what is the action after
// streams/{webId}/ (value, end, recorded, interpolated...).
func (s *PIServer) read(sr *src, what string, q map[string][]string) (any, *readErr) {
	now := s.now()
	if sr.pt == nil {
		if sr.attr == nil || sr.attr.point != "" {
			return nil, &readErr{404, "The PI point this attribute refers to was not found."}
		}
		switch what {
		case "value", "end":
			return s.staticJSON(sr.attr, sr.units), nil
		}
		return nil, &readErr{400, "The attribute has no data reference, so it has no archived values."}
	}
	vs := sr.pt.values
	step := sr.pt.typ != "Float32"
	if sr.attr != nil && sr.attr.step {
		step = true
	}
	flt, ok := parseFilter(first(q, "filterExpression"))
	if !ok {
		return nil, &readErr{400, "The simulator reads only filter expressions of the form 'tag' > number."}
	}
	includeFiltered := strings.EqualFold(first(q, "includeFilteredValues"), "true")
	keep := func(v PIValue) (PIValue, bool) {
		if flt.keep(v) {
			return v, true
		}
		if includeFiltered {
			return filtered(v.Time), true
		}
		return v, false
	}
	items := func(list []PIValue) map[string]any {
		out := make([]any, 0, len(list))
		for _, v := range list {
			out = append(out, s.valueJSON(sr, v))
		}
		return map[string]any{"Items": out, "UnitsAbbreviation": sr.units}
	}
	times := func(def time.Time) (time.Time, time.Time, *readErr) {
		start, ok1 := s.parsePITime(first(q, "startTime"), def)
		end, ok2 := s.parsePITime(first(q, "endTime"), now)
		if !ok1 || !ok2 {
			return start, end, &readErr{400, "Cannot parse the start or end time."}
		}
		return start, end, nil
	}
	atTimes := func() ([]time.Time, *readErr) {
		raw := all(q, "time")
		if len(raw) == 0 {
			return nil, &readErr{400, "Name at least one time."}
		}
		ts := make([]time.Time, len(raw))
		for i, r := range raw {
			t, ok := s.parsePITime(r, now)
			if !ok {
				return nil, &readErr{400, "Cannot parse the time '" + r + "'."}
			}
			ts[i] = t
		}
		if strings.EqualFold(first(q, "sortOrder"), "Descending") {
			sortTimes(ts, true)
		} else {
			sortTimes(ts, false)
		}
		return ts, nil
	}
	switch what {
	case "value":
		t, ok := s.parsePITime(first(q, "time"), now)
		if !ok {
			return nil, &readErr{400, "Cannot parse the time."}
		}
		if first(q, "time") == "" {
			// The snapshot: the last value at or before now.
			for i := len(vs) - 1; i >= 0; i-- {
				if !vs[i].Time.After(t) {
					return s.valueJSON(sr, vs[i]), nil
				}
			}
			return s.valueJSON(sr, noData(t)), nil
		}
		if v, ok := at(vs, t, step); ok {
			return s.valueJSON(sr, v), nil
		}
		if len(vs) > 0 && t.After(vs[len(vs)-1].Time) {
			v := vs[len(vs)-1]
			v.Time = t
			return s.valueJSON(sr, v), nil
		}
		return s.valueJSON(sr, noData(t)), nil
	case "end":
		if len(vs) == 0 {
			return s.valueJSON(sr, noData(now)), nil
		}
		return s.valueJSON(sr, vs[len(vs)-1]), nil
	case "recorded":
		start, end, err := times(now.Add(-24 * time.Hour))
		if err != nil {
			return nil, err
		}
		max, ok := intParam(q, "maxCount", 1000)
		if !ok {
			return nil, &readErr{400, "maxCount must be a non-negative integer."}
		}
		if max > s.MaxItems {
			return nil, &readErr{400, "maxCount exceeds the maximum number of items a call may return (" + strconv.Itoa(s.MaxItems) + ")."}
		}
		desc := end.Before(start)
		lo, hi := start, end
		if desc {
			lo, hi = end, start
		}
		var list []PIValue
		boundary := strings.ToLower(first(q, "boundaryType"))
		switch boundary {
		case "", "inside", "outside", "interpolated":
		default:
			return nil, &readErr{400, "boundaryType must be Inside, Outside or Interpolated."}
		}
		for i, v := range vs {
			in := !v.Time.Before(lo) && !v.Time.After(hi)
			if boundary == "outside" && !in {
				// The value just before the range and the one just after it.
				if v.Time.Before(lo) && (i+1 >= len(vs) || !vs[i+1].Time.Before(lo)) {
					in = true
				}
				if v.Time.After(hi) && (i == 0 || !vs[i-1].Time.After(hi)) {
					in = true
				}
			}
			if !in {
				continue
			}
			if kv, ok := keep(v); ok {
				list = append(list, kv)
			}
		}
		if boundary == "interpolated" {
			if v, ok := at(vs, lo, step); ok && (len(list) == 0 || !list[0].Time.Equal(lo)) {
				list = append([]PIValue{v}, list...)
			}
			if v, ok := at(vs, hi, step); ok && (len(list) == 0 || !list[len(list)-1].Time.Equal(hi)) {
				list = append(list, v)
			}
		}
		if desc {
			for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
				list[i], list[j] = list[j], list[i]
			}
		}
		if len(list) > max {
			list = list[:max]
		}
		return items(list), nil
	case "interpolated":
		start, end, err := times(now.Add(-24 * time.Hour))
		if err != nil {
			return nil, err
		}
		iv := first(q, "interval")
		if iv == "" {
			iv = "1h"
		}
		stepD, ok := parseSpan(iv)
		if !ok || stepD <= 0 {
			return nil, &readErr{400, "Cannot parse the interval."}
		}
		if sync := first(q, "syncTime"); sync != "" {
			st, ok := s.parsePITime(sync, start)
			if !ok {
				return nil, &readErr{400, "Cannot parse the sync time."}
			}
			// The first interval boundary at or after start (Inside, the
			// default) or before it (Outside).
			n := math.Ceil(start.Sub(st).Seconds() / stepD.Seconds())
			if strings.EqualFold(first(q, "syncTimeBoundaryType"), "Outside") {
				n = math.Floor(start.Sub(st).Seconds() / stepD.Seconds())
			}
			start = st.Add(time.Duration(n) * stepD)
		}
		if int(end.Sub(start)/stepD)+1 > s.MaxItems {
			return nil, &readErr{400, "The read would return more items than a call may (" + strconv.Itoa(s.MaxItems) + ")."}
		}
		var list []PIValue
		for t := start; !t.After(end); t = t.Add(stepD) {
			v, ok := at(vs, t, step)
			if !ok {
				continue
			}
			if kv, ok := keep(v); ok {
				list = append(list, kv)
			}
		}
		return items(list), nil
	case "interpolatedattimes":
		ts, err := atTimes()
		if err != nil {
			return nil, err
		}
		var list []PIValue
		for _, t := range ts {
			v, ok := at(vs, t, step)
			if !ok {
				v = noData(t)
			}
			if kv, ok := keep(v); ok {
				list = append(list, kv)
			}
		}
		return items(list), nil
	case "recordedattime":
		if first(q, "time") == "" {
			return nil, &readErr{400, "Name the time."}
		}
		t, ok := s.parsePITime(first(q, "time"), now)
		if !ok {
			return nil, &readErr{400, "Cannot parse the time."}
		}
		v, rerr := recordedAt(vs, t, first(q, "retrievalMode"))
		if rerr != nil {
			return nil, rerr
		}
		return s.valueJSON(sr, v), nil
	case "recordedattimes":
		ts, err := atTimes()
		if err != nil {
			return nil, err
		}
		var list []PIValue
		for _, t := range ts {
			v, rerr := recordedAt(vs, t, first(q, "retrievalMode"))
			if rerr != nil {
				return nil, rerr
			}
			list = append(list, v)
		}
		return items(list), nil
	case "plot":
		start, end, err := times(now.Add(-24 * time.Hour))
		if err != nil {
			return nil, err
		}
		n, ok := intParam(q, "intervals", 24)
		if !ok || n == 0 {
			return nil, &readErr{400, "intervals must be a positive integer."}
		}
		return items(plot(vs, start, end, n)), nil
	case "summary":
		start, end, err := times(now.Add(-24 * time.Hour))
		if err != nil {
			return nil, err
		}
		res, rerr := s.summary(sr, vs, step, start, end, q, flt)
		if rerr != nil {
			return nil, rerr
		}
		return map[string]any{"Items": res}, nil
	}
	return nil, &readErr{404, "The requested resource does not exist."}
}

func sortTimes(ts []time.Time, desc bool) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && (ts[j].Before(ts[j-1]) != desc) && !ts[j].Equal(ts[j-1]); j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

// recordedAt is the archived value at or near t, as mode says (Auto is
// the value at t, else the one before).
func recordedAt(vs []PIValue, t time.Time, mode string) (PIValue, *readErr) {
	before := func(incl bool) (PIValue, bool) {
		for i := len(vs) - 1; i >= 0; i-- {
			if vs[i].Time.Before(t) || (incl && vs[i].Time.Equal(t)) {
				return vs[i], true
			}
		}
		return PIValue{}, false
	}
	after := func(incl bool) (PIValue, bool) {
		for _, v := range vs {
			if v.Time.After(t) || (incl && v.Time.Equal(t)) {
				return v, true
			}
		}
		return PIValue{}, false
	}
	var v PIValue
	var ok bool
	switch strings.ToLower(mode) {
	case "", "auto", "atorbefore":
		v, ok = before(true)
	case "before":
		v, ok = before(false)
	case "atorafter":
		v, ok = after(true)
	case "after":
		v, ok = after(false)
	case "exact":
		v, ok = before(true)
		ok = ok && v.Time.Equal(t)
	default:
		return PIValue{}, &readErr{400, "retrievalMode must be Auto, AtOrBefore, Before, AtOrAfter, After or Exact."}
	}
	if !ok {
		return noData(t), nil
	}
	return v, nil
}

// plot keeps, for each of n intervals, the first, last, smallest and
// largest archived value, and every value that is not a number.
func plot(vs []PIValue, start, end time.Time, n int) []PIValue {
	if !end.After(start) {
		return nil
	}
	width := end.Sub(start) / time.Duration(n)
	if width <= 0 {
		width = 1
	}
	keep := map[int]bool{}
	bucket := -1
	var firstI, lastI, minI, maxI int
	flush := func() {
		if bucket < 0 {
			return
		}
		keep[firstI], keep[lastI] = true, true
		if minI >= 0 {
			keep[minI], keep[maxI] = true, true
		}
	}
	for i, v := range vs {
		if v.Time.Before(start) || v.Time.After(end) {
			continue
		}
		b := int(v.Time.Sub(start) / width)
		if b != bucket {
			flush()
			bucket, firstI, minI, maxI = b, i, -1, -1
		}
		lastI = i
		if !numeric(v) {
			keep[i] = true
			continue
		}
		if minI < 0 || v.Value < vs[minI].Value {
			minI = i
		}
		if maxI < 0 || v.Value > vs[maxI].Value {
			maxI = i
		}
	}
	flush()
	var out []PIValue
	for i, v := range vs {
		if keep[i] {
			out = append(out, v)
		}
	}
	return out
}

// summary computes the summaries of a stream over [start, end], split by
// summaryDuration. Average and Total are time-weighted (trapezoids for a
// Float point, steps otherwise; Total is the average times the duration
// in days, the PI convention) unless calculationBasis is EventWeighted,
// where they are the mean and the sum of the archived values. Minimum,
// Maximum and Range are over the archived values (and the interpolated
// boundaries, time-weighted); StdDev and PopulationStdDev are of the
// archived values; Count counts them; PercentGood is the share of good
// ones. Bad values are left out of the arithmetic. This is the
// simulator's arithmetic, not a copy of the PI System's.
func (s *PIServer) summary(sr *src, vs []PIValue, step bool, start, end time.Time, q map[string][]string, flt *filter) ([]any, *readErr) {
	types := all(q, "summaryType")
	if len(types) == 0 {
		types = []string{"Total"}
	}
	var expanded []string
	for _, t := range types {
		switch strings.ToLower(t) {
		case "all":
			expanded = append(expanded, "Total", "Average", "Minimum", "Maximum", "Range", "StdDev", "PopulationStdDev", "Count", "PercentGood")
		case "allfornonnumeric":
			expanded = append(expanded, "Count", "PercentGood")
		case "total", "average", "minimum", "maximum", "range", "stddev", "populationstddev", "count", "percentgood", "totalwithuom":
			expanded = append(expanded, canonical(t))
		default:
			return nil, &readErr{400, "Unknown summary type '" + t + "'."}
		}
	}
	eventWeighted := strings.EqualFold(first(q, "calculationBasis"), "EventWeighted")
	dur := end.Sub(start)
	if d := first(q, "summaryDuration"); d != "" {
		var ok bool
		if dur, ok = parseSpan(d); !ok || dur <= 0 {
			return nil, &readErr{400, "Cannot parse the summary duration."}
		}
	}
	if !end.After(start) {
		return nil, &readErr{400, "The end time must be after the start time."}
	}
	var out []any
	for _, typ := range expanded {
		for a := start; a.Before(end); a = a.Add(dur) {
			b := a.Add(dur)
			if b.After(end) {
				b = end
			}
			v := s.summarize(vs, step, a, b, typ, eventWeighted, flt)
			units := sr.units
			if typ == "Count" || typ == "PercentGood" {
				units = ""
			}
			val := s.valueJSON(&src{units: units, pt: sr.pt}, v)
			out = append(out, map[string]any{"Type": typ, "Value": val})
		}
	}
	return out, nil
}

func canonical(t string) string {
	for _, c := range []string{"Total", "Average", "Minimum", "Maximum", "Range", "StdDev", "PopulationStdDev", "Count", "PercentGood", "TotalWithUOM"} {
		if strings.EqualFold(c, t) {
			return c
		}
	}
	return t
}

func (s *PIServer) summarize(vs []PIValue, step bool, a, b time.Time, typ string, eventWeighted bool, flt *filter) PIValue {
	var in []PIValue
	total := 0
	for _, v := range vs {
		if v.Time.Before(a) || v.Time.After(b) {
			continue
		}
		total++
		if numeric(v) && !v.Bad && flt.keep(v) {
			in = append(in, v)
		}
	}
	res := PIValue{Time: a}
	switch typ {
	case "Count":
		res.Value = float64(len(in))
		return res
	case "PercentGood":
		if total == 0 {
			return noData(a)
		}
		res.Value = 100 * float64(len(in)) / float64(total)
		return res
	}
	// Time-weighted reads take the interpolated values at the boundaries.
	pts := in
	if !eventWeighted {
		pts = nil
		if v, ok := at(vs, a, step); ok && numeric(v) && !v.Bad && (len(in) == 0 || !in[0].Time.Equal(a)) {
			pts = append(pts, v)
		}
		pts = append(pts, in...)
		if v, ok := at(vs, b, step); ok && numeric(v) && !v.Bad && (len(pts) == 0 || !pts[len(pts)-1].Time.Equal(b)) {
			pts = append(pts, v)
		}
	}
	if len(pts) == 0 {
		return noData(a)
	}
	mean, sum := 0.0, 0.0
	for _, v := range in {
		sum += v.Value
	}
	if len(in) > 0 {
		mean = sum / float64(len(in))
	}
	switch typ {
	case "Minimum", "Maximum":
		best := pts[0]
		for _, v := range pts[1:] {
			if (typ == "Minimum" && v.Value < best.Value) || (typ == "Maximum" && v.Value > best.Value) {
				best = v
			}
		}
		best.Annotated = false
		return best
	case "Range":
		lo, hi := pts[0].Value, pts[0].Value
		for _, v := range pts {
			lo, hi = math.Min(lo, v.Value), math.Max(hi, v.Value)
		}
		res.Value = hi - lo
	case "StdDev", "PopulationStdDev":
		n := float64(len(in))
		if n < 2 && typ == "StdDev" || n == 0 {
			return noData(a)
		}
		ss := 0.0
		for _, v := range in {
			ss += (v.Value - mean) * (v.Value - mean)
		}
		d := n
		if typ == "StdDev" {
			d = n - 1
		}
		res.Value = math.Round(math.Sqrt(ss/d)*1e6) / 1e6
	case "Average", "Total", "TotalWithUOM":
		if eventWeighted {
			if len(in) == 0 {
				return noData(a)
			}
			res.Value = mean
			if typ != "Average" {
				res.Value = sum
			}
			return res
		}
		area := 0.0
		for i := 0; i+1 < len(pts); i++ {
			dt := pts[i+1].Time.Sub(pts[i].Time).Seconds()
			if step {
				area += pts[i].Value * dt
			} else {
				area += (pts[i].Value + pts[i+1].Value) / 2 * dt
			}
		}
		secs := b.Sub(a).Seconds()
		avg := pts[0].Value
		if secs > 0 && len(pts) > 1 {
			avg = area / pts[len(pts)-1].Time.Sub(pts[0].Time).Seconds()
		}
		res.Value = math.Round(avg*1e6) / 1e6
		if typ != "Average" {
			res.Value = math.Round(avg*secs/86400*1e6) / 1e6
		}
	}
	return res
}

func (s *PIServer) routeStreams(w http.ResponseWriter, method string, parts []string, q map[string][]string, body []byte) {
	if parts[0] == "streams" {
		switch {
		case len(parts) == 3 && parts[1] == "updates" && method == http.MethodGet:
			s.updates(w, parts[2])
			return
		case len(parts) != 3:
			piError(w, 404, "The requested resource does not exist.")
			return
		}
		sr := s.source(parts[1])
		if sr == nil {
			piError(w, 404, "The specified WebId is not valid or the object was not found.")
			return
		}
		switch {
		case method == http.MethodGet && parts[2] != "updates":
			res, err := s.read(sr, parts[2], q)
			if err != nil {
				piError(w, err.code, err.msg)
				return
			}
			writeJSON(w, 200, res)
		case method == http.MethodPost && parts[2] == "value":
			var tv timedValue
			if json.Unmarshal(body, &tv) != nil {
				piError(w, 400, "The request body is not a timed value.")
				return
			}
			if err := s.write(sr, tv, q); err != nil {
				piError(w, err.code, err.msg)
				return
			}
			writeJSON(w, writeStatus(q), nil)
		case method == http.MethodPost && parts[2] == "recorded":
			var tvs []timedValue
			if json.Unmarshal(body, &tvs) != nil {
				piError(w, 400, "The request body is not a list of timed values.")
				return
			}
			var subs []any
			failed := 0
			for _, tv := range tvs {
				sub, bad := substatus(s.write(sr, tv, q), q)
				subs = append(subs, sub)
				if bad {
					failed++
				}
			}
			s.multiStatus(w, q, subs, failed)
		case method == http.MethodPost && parts[2] == "updates":
			writeJSON(w, 202, s.register(sr))
		default:
			piError(w, 405, "The simulator does not support this method here.")
		}
		return
	}
	s.streamSets(w, method, parts, q, body)
}

// writeStatus is 204 for an unbuffered write, 202 for one that may be
// buffered.
func writeStatus(q map[string][]string) int {
	if strings.EqualFold(first(q, "bufferOption"), "DoNotBuffer") {
		return http.StatusNoContent
	}
	return http.StatusAccepted
}

func substatus(err *readErr, q map[string][]string) (map[string]any, bool) {
	if err == nil {
		return map[string]any{"Substatus": writeStatus(q)}, false
	}
	return map[string]any{"Substatus": err.code, "Message": err.msg,
		"WebException": map[string]any{"StatusCode": err.code, "Errors": []string{err.msg}}}, true
}

// multiStatus answers a write of several items: 202 or 204 when all were
// written, 207 with each item's substatus when some failed.
func (s *PIServer) multiStatus(w http.ResponseWriter, q map[string][]string, items []any, failed int) {
	if failed == 0 {
		writeJSON(w, writeStatus(q), nil)
		return
	}
	if items == nil {
		items = []any{}
	}
	writeJSON(w, http.StatusMultiStatus, map[string]any{"Items": items})
}

type timedValue struct {
	Timestamp    string          `json:"Timestamp"`
	Value        json.RawMessage `json:"Value"`
	Good         *bool           `json:"Good"`
	Questionable *bool           `json:"Questionable"`
	Units        string          `json:"UnitsAbbreviation"`
}

// write applies one value to a stream with the request's updateOption.
func (s *PIServer) write(sr *src, tv timedValue, q map[string][]string) *readErr {
	t, ok := s.parsePITime(tv.Timestamp, s.now())
	if !ok {
		return &readErr{400, "Cannot parse the timestamp '" + tv.Timestamp + "'."}
	}
	if sr.pt == nil {
		if sr.attr != nil && sr.attr.point == "" {
			var x any
			if json.Unmarshal(tv.Value, &x) != nil {
				return &readErr{400, "The value does not read."}
			}
			sr.attr.static, sr.attr.staticTime = x, t
			return nil
		}
		return &readErr{404, "The PI point this attribute refers to was not found."}
	}
	p := sr.pt
	if p.readOnly {
		return &readErr{403, "The login may not write to this PI point."}
	}
	v, rerr := p.decode(tv.Value)
	if rerr != nil {
		return rerr
	}
	v.Time = t
	if tv.Good != nil && !*tv.Good {
		v.Bad = true
	}
	if tv.Questionable != nil {
		v.Questionable = *tv.Questionable
	}
	idx := -1
	for i, x := range p.values {
		if x.Time.Equal(t) {
			idx = i
		}
	}
	action := "Add"
	switch strings.ToLower(first(q, "updateOption")) {
	case "", "replace":
		if idx >= 0 {
			p.values[idx], action = v, "Update"
		} else {
			p.values = append(p.values, v)
		}
	case "insert", "insertnocompression":
		p.values = append(p.values, v)
	case "noreplace":
		if idx >= 0 {
			return &readErr{409, "A value already exists at " + t.Format(time.RFC3339Nano) + "."}
		}
		p.values = append(p.values, v)
	case "replaceonly":
		if idx < 0 {
			return &readErr{409, "No value exists at " + t.Format(time.RFC3339Nano) + " to replace."}
		}
		p.values[idx], action = v, "Update"
	case "remove":
		if idx < 0 {
			return &readErr{404, "No value exists at " + t.Format(time.RFC3339Nano) + " to remove."}
		}
		v = p.values[idx]
		p.values = append(p.values[:idx], p.values[idx+1:]...)
		action = "Remove"
	default:
		return &readErr{400, "Unknown updateOption '" + first(q, "updateOption") + "'."}
	}
	sortValues(p.values)
	p.code(v)
	s.logEvent(s.pointWebID(p), v, action)
	if sr.attr != nil {
		s.logEvent(sr.webID, v, action)
	}
	return nil
}

// decode reads a written value for the point's type: a number for a
// Float point, text for a String point, and for a Digital point a state
// by name ({"Name": ...} or a string) or by code.
func (p *piPoint) decode(raw json.RawMessage) (PIValue, *readErr) {
	var x any
	if json.Unmarshal(raw, &x) != nil {
		return PIValue{}, &readErr{400, "The value does not read."}
	}
	switch p.typ {
	case "Float32":
		switch v := x.(type) {
		case float64:
			return PIValue{Value: v}, nil
		case map[string]any:
			if n, _ := v["Name"].(string); n != "" {
				if sys, _ := v["IsSystem"].(bool); sys {
					return PIValue{State: n, System: true}, nil
				}
			}
		}
		return PIValue{}, &readErr{400, "The value must be a number for a Float32 point."}
	case "String":
		if v, ok := x.(string); ok {
			return PIValue{Text: v}, nil
		}
		return PIValue{}, &readErr{400, "The value must be text for a String point."}
	}
	name := ""
	switch v := x.(type) {
	case string:
		name = v
	case map[string]any:
		name, _ = v["Name"].(string)
		if sys, _ := v["IsSystem"].(bool); sys && name != "" {
			return PIValue{State: name, System: true}, nil
		}
		if name == "" {
			if c, ok := v["Value"].(float64); ok {
				x = c
			}
		}
	}
	if c, ok := x.(float64); ok && name == "" {
		if int(c) >= 0 && int(c) < len(p.states) && float64(int(c)) == c {
			name = p.states[int(c)]
		} else {
			return PIValue{}, &readErr{400, "No digital state has code " + strconv.FormatFloat(c, 'f', -1, 64) + "."}
		}
	}
	for _, st := range p.states {
		if strings.EqualFold(st, name) {
			return PIValue{State: st}, nil
		}
	}
	return PIValue{}, &readErr{400, "'" + name + "' is not a state of this point's digital set."}
}

type updateEvent struct {
	seq    int
	webID  string
	v      PIValue
	action string
}

func (s *PIServer) logEvent(id string, v PIValue, action string) {
	s.seq++
	s.events = append(s.events, updateEvent{seq: s.seq, webID: id, v: v, action: action})
}

func marker(id string, seq int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id + "|" + strconv.Itoa(seq)))
}

func parseMarker(m string) (string, int, bool) {
	b, err := base64.RawURLEncoding.DecodeString(m)
	if err != nil {
		return "", 0, false
	}
	id, n, ok := strings.Cut(string(b), "|")
	seq, err := strconv.Atoi(n)
	return id, seq, ok && err == nil
}

func (s *PIServer) register(sr *src) map[string]any {
	return map[string]any{"Source": sr.webID, "SourceName": sr.name, "SourcePath": sr.path, "Status": "Succeeded", "LatestMarker": marker(sr.webID, s.seq)}
}

func (s *PIServer) updatesOf(m string) (map[string]any, *readErr) {
	id, seq, ok := parseMarker(m)
	if !ok {
		return nil, &readErr{400, "The marker is not valid."}
	}
	sr := s.source(id)
	if sr == nil {
		return nil, &readErr{404, "The marker's stream was not found."}
	}
	events := []any{}
	for _, e := range s.events {
		if e.seq > seq && e.webID == id {
			ev := s.valueJSON(sr, e.v)
			ev["Action"] = e.action
			events = append(events, ev)
		}
	}
	return map[string]any{"Source": id, "SourceName": sr.name, "SourcePath": sr.path, "Status": "Succeeded", "Events": events,
		"RequestedMarker": m, "LatestMarker": marker(id, s.seq)}, nil
}

func (s *PIServer) updates(w http.ResponseWriter, m string) {
	res, err := s.updatesOf(m)
	if err != nil {
		piError(w, err.code, err.msg)
		return
	}
	writeJSON(w, 200, res)
}
