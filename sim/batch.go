// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type batchReq struct {
	Method          string                     `json:"Method"`
	Resource        string                     `json:"Resource"`
	RequestTemplate *struct{ Resource string } `json:"RequestTemplate"`
	Parameters      []string                   `json:"Parameters"`
	Headers         map[string]string          `json:"Headers"`
	Content         string                     `json:"Content"`
	ParentIDs       []string                   `json:"ParentIds"`
}

// batch answers POST batch: the requests run in an order that puts each
// after its parents; Parameters are JSONPath expressions into earlier
// responses ($.ID.Content.Field, $.ID.Content.Items[*].Field,
// $.ID.Content.Items[0].Field, $.ID.Status) that fill {0}, {1}... in
// Resource. With RequestTemplate the request runs once per value of the
// first parameter and its Content is {"Items": [responses]}. When a
// value fills a query parameter (name={0}) and the expression selects
// several values, the parameter is repeated once per value. A request
// whose parent did not succeed is not run and answers 409 (the
// simulator's choice). The batch answers 207 when a request failed, else
// 200. A batch naming an unknown parent, or whose parents form a cycle,
// is refused with 400.
func (s *PIServer) batch(w http.ResponseWriter, body []byte, depth int) {
	var reqs map[string]batchReq
	if err := json.Unmarshal(body, &reqs); err != nil || len(reqs) == 0 {
		piError(w, 400, "The batch body is not an object of requests.")
		return
	}
	ids := make([]string, 0, len(reqs))
	for id, r := range reqs {
		ids = append(ids, id)
		for _, p := range r.ParentIDs {
			if _, ok := reqs[p]; !ok {
				piError(w, 400, "Request "+id+" names parent "+p+", which is not in the batch.")
				return
			}
		}
	}
	sort.Strings(ids)
	var order []string
	done := map[string]bool{}
	for len(order) < len(ids) {
		progress := false
		for _, id := range ids {
			if done[id] {
				continue
			}
			ready := true
			for _, p := range reqs[id].ParentIDs {
				ready = ready && done[p]
			}
			if ready {
				done[id], progress = true, true
				order = append(order, id)
			}
		}
		if !progress {
			piError(w, 400, "The batch's parent requests form a cycle.")
			return
		}
	}
	results := map[string]map[string]any{}
	failed := false
	for _, id := range order {
		r := reqs[id]
		res := s.batchOne(r, results, depth)
		if st, _ := res["Status"].(int); st/100 != 2 {
			failed = true
		}
		results[id] = res
	}
	status := 200
	if failed {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, results)
}

func batchFail(code int, msg string) map[string]any {
	return map[string]any{"Status": code, "Headers": map[string]string{"Content-Type": "application/json; charset=utf-8"},
		"Content": map[string]any{"Errors": []string{msg}}}
}

func (s *PIServer) batchOne(r batchReq, results map[string]map[string]any, depth int) map[string]any {
	for _, p := range r.ParentIDs {
		if st, _ := results[p]["Status"].(int); st/100 != 2 {
			return batchFail(409, "Parent request "+p+" did not succeed.")
		}
	}
	if r.Method == "" {
		return batchFail(400, "The request has no Method.")
	}
	var params [][]string
	for _, expr := range r.Parameters {
		vals, err := jsonPath(results, expr)
		if err != nil {
			return batchFail(400, err.Error())
		}
		params = append(params, vals)
	}
	if r.RequestTemplate != nil {
		if len(params) == 0 {
			return batchFail(400, "A RequestTemplate needs a parameter.")
		}
		items := []any{}
		anyFailed := false
		for _, v := range params[0] {
			fill := [][]string{{v}}
			for _, p := range params[1:] {
				fill = append(fill, p)
			}
			res := s.batchRun(r.Method, substitute(r.RequestTemplate.Resource, fill), r.Content, depth)
			if st, _ := res["Status"].(int); st/100 != 2 {
				anyFailed = true
			}
			items = append(items, res)
		}
		status := 200
		if anyFailed {
			status = http.StatusMultiStatus
		}
		return map[string]any{"Status": status, "Headers": map[string]string{"Content-Type": "application/json; charset=utf-8"},
			"Content": map[string]any{"Items": items}}
	}
	if r.Resource == "" {
		return batchFail(400, "The request has no Resource.")
	}
	return s.batchRun(r.Method, substitute(r.Resource, params), r.Content, depth)
}

// substitute fills {i} with the values of parameter i: in a query
// (name={i}) each value as its own name=value, elsewhere the first value.
func substitute(resource string, params [][]string) string {
	for i, vals := range params {
		ph := "{" + strconv.Itoa(i) + "}"
		for strings.Contains(resource, ph) {
			at := strings.Index(resource, ph)
			before := resource[:at]
			inQuery := strings.Contains(before, "?")
			if inQuery {
				eq := strings.LastIndexAny(before, "&?")
				name := before[eq+1:]
				if strings.HasSuffix(name, "=") && len(vals) > 0 {
					var parts []string
					for _, v := range vals {
						parts = append(parts, name+url.QueryEscape(v))
					}
					resource = before[:eq+1] + strings.Join(parts, "&") + resource[at+len(ph):]
					continue
				}
			}
			v := ""
			if len(vals) > 0 {
				v = vals[0]
			}
			if inQuery {
				v = url.QueryEscape(v)
			} else {
				v = url.PathEscape(v)
			}
			resource = before + v + resource[at+len(ph):]
		}
	}
	return resource
}

// batchRun runs one sub-request against the simulator itself.
func (s *PIServer) batchRun(method, resource, content string, depth int) map[string]any {
	u, err := url.Parse(resource)
	if err != nil {
		return batchFail(400, "The Resource is not a URL.")
	}
	path := u.Path
	if i := strings.Index(path, "/piwebapi"); i >= 0 {
		path = path[i+len("/piwebapi"):]
	}
	path = strings.Trim(path, "/")
	if f := s.faultLocked(method, path); f != nil && f.Status != 0 {
		msg := f.Message
		if msg == "" {
			msg = http.StatusText(f.Status)
		}
		return batchFail(f.Status, msg)
	}
	rec := httptest.NewRecorder()
	s.route(rec, strings.ToUpper(method), path, u.Query(), []byte(content), depth+1)
	res := map[string]any{"Status": rec.Code, "Headers": map[string]string{"Content-Type": rec.Header().Get("Content-Type")}}
	if b := strings.TrimSpace(rec.Body.String()); b != "" {
		res["Content"] = json.RawMessage(b)
	}
	return res
}

// jsonPath evaluates $.ID.Field.Field[n].Field[*]... against the batch
// results so far.
func jsonPath(results map[string]map[string]any, expr string) ([]string, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(expr), "$.")
	if !ok {
		return nil, fmt.Errorf("parameter %q is not a JSONPath starting with $.", expr)
	}
	var cur []any
	toks := splitPath(rest)
	if len(toks) == 0 {
		return nil, fmt.Errorf("parameter %q names no request", expr)
	}
	root, ok := results[toks[0].name]
	if !ok {
		return nil, fmt.Errorf("parameter %q names request %s, which is not a parent that ran", expr, toks[0].name)
	}
	var top any
	b, _ := json.Marshal(root)
	_ = json.Unmarshal(b, &top)
	cur = []any{top}
	for _, t := range toks[1:] {
		var next []any
		for _, c := range cur {
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			v, ok := m[t.name]
			if !ok {
				continue
			}
			switch {
			case t.index == "":
				next = append(next, v)
			case t.index == "*":
				if list, ok := v.([]any); ok {
					next = append(next, list...)
				}
			default:
				n, err := strconv.Atoi(t.index)
				if list, ok := v.([]any); ok && err == nil && n >= 0 && n < len(list) {
					next = append(next, list[n])
				}
			}
		}
		cur = next
	}
	out := make([]string, 0, len(cur))
	for _, c := range cur {
		switch v := c.(type) {
		case string:
			out = append(out, v)
		case nil:
		default:
			b, _ := json.Marshal(v)
			out = append(out, string(b))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parameter %q selects nothing", expr)
	}
	return out, nil
}

type pathTok struct{ name, index string }

func splitPath(p string) []pathTok {
	var out []pathTok
	for _, part := range strings.Split(p, ".") {
		t := pathTok{name: part}
		if i := strings.Index(part, "["); i >= 0 && strings.HasSuffix(part, "]") {
			t.name, t.index = part[:i], part[i+1:len(part)-1]
		}
		out = append(out, t)
	}
	return out
}
