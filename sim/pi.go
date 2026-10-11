// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *PIServer) dataServerPath() string { return `\\PIServers[` + s.Archive + `]` }

func (s *PIServer) dataServerWebID() string { return webID("DS", "", s.dataServerPath()) }

func (s *PIServer) pointPath(p *piPoint) string { return `\\` + s.Archive + `\` + p.name }

func (s *PIServer) pointWebID(p *piPoint) string { return webID("DP", "", s.pointPath(p)) }

func (s *PIServer) dataServerJSON() map[string]any {
	return map[string]any{"WebId": s.dataServerWebID(), "Id": "8c0c6bfb-0000-4000-8000-000000000001", "Name": s.Archive,
		"Path": s.dataServerPath(), "IsConnected": true, "ServerVersion": "3.4.445.688"}
}

func (s *PIServer) pointJSON(p *piPoint) map[string]any {
	m := map[string]any{"WebId": s.pointWebID(p), "Id": p.id, "Name": p.name,
		"Path": s.pointPath(p), "Descriptor": p.desc, "PointClass": "classic", "PointType": p.typ,
		"EngineeringUnits": p.units, "Step": p.typ != "Float32", "Future": false}
	if p.typ == "Digital" {
		m["DigitalSetName"] = p.name + ".STATES"
	}
	return m
}

func (s *PIServer) pointByWebID(id string) *piPoint {
	for _, p := range s.points {
		if s.pointWebID(p) == id {
			return p
		}
	}
	return nil
}

// pointByPath finds \\ARCHIVE\NAME (case-insensitive, as PI names are).
func (s *PIServer) pointByPath(path string) (*piPoint, string) {
	parts := strings.Split(strings.TrimPrefix(path, `\\`), `\`)
	if len(parts) != 2 || !strings.EqualFold(parts[0], s.Archive) {
		return nil, "PI Data Archive not found for path '" + path + "'."
	}
	for n, p := range s.points {
		if strings.EqualFold(n, parts[1]) {
			return p, ""
		}
	}
	return nil, "PI Point not found '" + path + "'."
}

func intParam(q map[string][]string, k string, def int) (int, bool) {
	v := first(q, k)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

func first(q map[string][]string, k string) string {
	for key, vs := range q {
		if strings.EqualFold(key, k) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

func all(q map[string][]string, k string) []string {
	var out []string
	for key, vs := range q {
		if strings.EqualFold(key, k) {
			out = append(out, vs...)
		}
	}
	return out
}

// page cuts a list by startIndex and maxCount (default 1000).
func (s *PIServer) page(w http.ResponseWriter, q map[string][]string, items []any) ([]any, bool) {
	start, ok1 := intParam(q, "startIndex", 0)
	max, ok2 := intParam(q, "maxCount", 1000)
	if !ok1 || !ok2 {
		piError(w, 400, "startIndex and maxCount must be non-negative integers.")
		return nil, false
	}
	if max > s.MaxItems {
		piError(w, 400, "maxCount exceeds the maximum number of items a call may return ("+strconv.Itoa(s.MaxItems)+").")
		return nil, false
	}
	if start > len(items) {
		start = len(items)
	}
	items = items[start:]
	if len(items) > max {
		items = items[:max]
	}
	if items == nil {
		items = []any{}
	}
	return items, true
}

func (s *PIServer) routePI(w http.ResponseWriter, method string, parts []string, q map[string][]string) {
	if method != http.MethodGet {
		piError(w, 405, "The simulator does not change Data Archive configuration.")
		return
	}
	switch {
	case parts[0] == "dataservers" && len(parts) == 1:
		switch {
		case first(q, "name") != "":
			if !strings.EqualFold(first(q, "name"), s.Archive) {
				piError(w, 404, "PI Data Archive not found '"+first(q, "name")+"'.")
				return
			}
			writeJSON(w, 200, s.dataServerJSON())
		case first(q, "path") != "":
			if !strings.EqualFold(first(q, "path"), s.dataServerPath()) {
				piError(w, 404, "PI Data Archive not found for path '"+first(q, "path")+"'.")
				return
			}
			writeJSON(w, 200, s.dataServerJSON())
		default:
			writeJSON(w, 200, map[string]any{"Items": []any{s.dataServerJSON()}})
		}
	case parts[0] == "dataservers" && parts[1] != s.dataServerWebID():
		piError(w, 404, "The specified WebId is not valid or the object was not found.")
	case parts[0] == "dataservers" && len(parts) == 2:
		writeJSON(w, 200, s.dataServerJSON())
	case parts[0] == "dataservers" && len(parts) == 3 && parts[2] == "points":
		filter := first(q, "nameFilter")
		names := make([]string, 0, len(s.points))
		for n := range s.points {
			if match(filter, n) {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		items := make([]any, len(names))
		for i, n := range names {
			items[i] = s.pointJSON(s.points[n])
		}
		if items, ok := s.page(w, q, items); ok {
			writeJSON(w, 200, map[string]any{"Items": items})
		}
	case parts[0] == "points" && len(parts) == 1:
		p, msg := s.pointByPath(first(q, "path"))
		if p == nil {
			piError(w, 404, msg)
			return
		}
		writeJSON(w, 200, s.pointJSON(p))
	case parts[0] == "points" && len(parts) == 2 && parts[1] == "multiple":
		s.multiple(w, q, func(path string) (any, string) {
			p, msg := s.pointByPath(path)
			if p == nil {
				return nil, msg
			}
			return s.pointJSON(p), ""
		}, func(id string) (any, string) {
			if p := s.pointByWebID(id); p != nil {
				return s.pointJSON(p), ""
			}
			return nil, "The specified WebId is not valid or the object was not found."
		})
	case parts[0] == "points" && len(parts) >= 2:
		p := s.pointByWebID(parts[1])
		if p == nil {
			piError(w, 404, "The specified WebId is not valid or the object was not found.")
			return
		}
		switch {
		case len(parts) == 2:
			writeJSON(w, 200, s.pointJSON(p))
		case len(parts) == 3 && parts[2] == "attributes":
			step := 0
			if p.typ != "Float32" {
				step = 1
			}
			attrs := []struct {
				n string
				v any
			}{{"tag", p.name}, {"descriptor", p.desc}, {"engunits", p.units}, {"pointtype", p.typ}, {"pointsource", "L"},
				{"zero", 0}, {"span", 100}, {"step", step}, {"compressing", 1}, {"excdev", 0.5}, {"compdev", 1}}
			filter := first(q, "nameFilter")
			names := all(q, "name")
			items := []any{}
			for _, a := range attrs {
				if !match(filter, a.n) {
					continue
				}
				if len(names) > 0 && !containsFold(names, a.n) {
					continue
				}
				items = append(items, map[string]any{"Name": a.n, "Value": a.v})
			}
			writeJSON(w, 200, map[string]any{"Items": items})
		default:
			piError(w, 404, "The requested resource does not exist.")
		}
	default:
		piError(w, 404, "The requested resource does not exist.")
	}
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// multiple answers {kind}/multiple?path=...&webId=... with one item per
// identifier, an Exception for those not found.
func (s *PIServer) multiple(w http.ResponseWriter, q map[string][]string, byPath, byID func(string) (any, string)) {
	items := []any{}
	add := func(id, typ string, look func(string) (any, string)) {
		obj, msg := look(id)
		it := map[string]any{"Identifier": id, "IdentifierType": typ}
		if obj != nil {
			it["Object"] = obj
		} else {
			it["Exception"] = map[string]any{"Errors": []string{msg}}
		}
		items = append(items, it)
	}
	for _, p := range all(q, "path") {
		add(p, "Path", byPath)
	}
	for _, id := range all(q, "webId") {
		add(id, "WebId", byID)
	}
	if len(items) == 0 {
		piError(w, 400, "Name at least one path or webId.")
		return
	}
	writeJSON(w, 200, map[string]any{"Items": items})
}

var relTime = regexp.MustCompile(`^(\*|t|y|today|yesterday)?\s*(?:([+-])\s*([0-9.]+)\s*([a-z]+))?$`)

// parsePITime reads an absolute time (ISO 8601) or a PI time expression:
// "*" (now), "t" or "today" (midnight UTC), "y" or "yesterday", each
// optionally followed by +/- a span ("*-1h", "t+6h", "*-2d"). Empty is
// def.
func (s *PIServer) parsePITime(v string, def time.Time) (time.Time, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return def, true
	}
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02"} {
		if t, err := time.Parse(l, strings.ToUpper(v)); err == nil {
			return t.UTC(), true
		}
	}
	m := relTime.FindStringSubmatch(v)
	if m == nil || (m[1] == "" && m[2] == "") {
		return time.Time{}, false
	}
	now := s.now()
	base := now
	switch m[1] {
	case "t", "today":
		base = now.Truncate(24 * time.Hour)
	case "y", "yesterday":
		base = now.Truncate(24 * time.Hour).Add(-24 * time.Hour)
	case "":
		base = now
	}
	if m[2] == "" {
		return base, true
	}
	d, ok := parseSpan(m[3] + m[4])
	if !ok {
		return time.Time{}, false
	}
	if m[2] == "-" {
		d = -d
	}
	return base.Add(d), true
}

var spanRe = regexp.MustCompile(`^([0-9.]+)\s*([a-z]+)$`)

// parseSpan reads a PI time span ("10s", "5m", "1h", "2d", "1w", "30
// minutes") or a Go duration ("1h30m").
func parseSpan(v string) (time.Duration, bool) {
	v = strings.TrimSpace(strings.ToLower(v))
	if m := spanRe.FindStringSubmatch(v); m != nil {
		n, err := strconv.ParseFloat(m[1], 64)
		if err == nil {
			var unit time.Duration
			switch m[2] {
			case "s", "sec", "secs", "second", "seconds":
				unit = time.Second
			case "m", "min", "mins", "minute", "minutes":
				unit = time.Minute
			case "h", "hr", "hrs", "hour", "hours":
				unit = time.Hour
			case "d", "day", "days":
				unit = 24 * time.Hour
			case "w", "week", "weeks":
				unit = 7 * 24 * time.Hour
			case "ms":
				unit = time.Millisecond
			}
			if unit != 0 {
				return time.Duration(n * float64(unit)), true
			}
		}
	}
	d, err := time.ParseDuration(v)
	return d, err == nil
}
