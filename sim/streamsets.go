// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// streamSets answers streamsets/... Ad hoc sets name their streams by
// webId; streamsets/{webId}/... reads the attributes of an element or
// event frame. A stream that cannot be read is an item with a
// WebException, the others are read.
func (s *PIServer) streamSets(w http.ResponseWriter, method string, parts []string, q map[string][]string, body []byte) {
	if len(parts) == 2 && parts[1] == "updates" {
		switch method {
		case http.MethodPost:
			items := []any{}
			for _, id := range all(q, "webId") {
				if sr := s.source(id); sr != nil {
					items = append(items, s.register(sr))
				} else {
					items = append(items, map[string]any{"Source": id, "Status": "Failed",
						"Exception": map[string]any{"StatusCode": 404, "Errors": []string{"The specified WebId is not valid or the object was not found."}}})
				}
			}
			writeJSON(w, 202, map[string]any{"Items": items})
		case http.MethodGet:
			items := []any{}
			for _, m := range all(q, "marker") {
				res, err := s.updatesOf(m)
				if err != nil {
					res = map[string]any{"RequestedMarker": m, "Status": "Failed", "Exception": map[string]any{"StatusCode": err.code, "Errors": []string{err.msg}}}
				}
				items = append(items, res)
			}
			writeJSON(w, 200, map[string]any{"Items": items})
		default:
			piError(w, 405, "The simulator does not support this method here.")
		}
		return
	}
	var what string
	var srcs []*src
	var missing []string
	switch len(parts) {
	case 2:
		what = parts[1]
		if method == http.MethodGet {
			for _, id := range all(q, "webId") {
				if sr := s.source(id); sr != nil {
					srcs = append(srcs, sr)
				} else {
					srcs = append(srcs, nil)
					missing = append(missing, id)
				}
			}
			if len(srcs) == 0 {
				piError(w, 400, "Name at least one webId.")
				return
			}
		}
	case 3:
		what = parts[2]
		attrs, ok := s.attrsOf(parts[1])
		if !ok {
			piError(w, 404, "The specified WebId is not an element or event frame, or it was not found.")
			return
		}
		full := strings.EqualFold(first(q, "searchFullHierarchy"), "true")
		var walk func([]*afAttr)
		walk = func(list []*afAttr) {
			for _, a := range list {
				if attrMatches(a, q) {
					srcs = append(srcs, s.attrSrc(a))
				}
				if full {
					walk(a.children)
				}
			}
		}
		walk(attrs)
		if strings.EqualFold(first(q, "sortOrder"), "Descending") && first(q, "time") == "" {
			sort.SliceStable(srcs, func(i, j int) bool { return srcs[i].name > srcs[j].name })
		}
	default:
		piError(w, 404, "The requested resource does not exist.")
		return
	}
	if method == http.MethodPost {
		s.streamSetWrite(w, what, srcs, q, body)
		return
	}
	if method != http.MethodGet {
		piError(w, 405, "The simulator does not support this method here.")
		return
	}
	single := what == "value" || what == "end" || what == "recordedattime"
	items := []any{}
	mi := 0
	for _, sr := range srcs {
		if sr == nil {
			id := missing[mi]
			mi++
			items = append(items, map[string]any{"WebId": id, "WebException": map[string]any{"StatusCode": 404,
				"Errors": []string{"The specified WebId is not valid or the object was not found."}}})
			continue
		}
		head := map[string]any{"WebId": sr.webID, "Name": sr.name, "Path": sr.path}
		res, err := s.read(sr, what, q)
		if err != nil {
			if err.code == 404 && strings.Contains(err.msg, "requested resource") {
				piError(w, 404, err.msg)
				return
			}
			if err.code == 400 && (sr.pt != nil || !strings.Contains(err.msg, "no data reference")) {
				// A bad parameter fails the whole read.
				piError(w, err.code, err.msg)
				return
			}
			head["WebException"] = map[string]any{"StatusCode": err.code, "Errors": []string{err.msg}}
			items = append(items, head)
			continue
		}
		if single {
			head["Value"] = res
			if m, ok := res.(map[string]any); ok {
				head["UnitsAbbreviation"] = m["UnitsAbbreviation"]
			}
		} else {
			m := res.(map[string]any)
			head["Items"] = m["Items"]
			if u, ok := m["UnitsAbbreviation"]; ok {
				head["UnitsAbbreviation"] = u
			}
		}
		items = append(items, head)
	}
	writeJSON(w, 200, map[string]any{"Items": items})
}

// attrMatches applies a stream set's attribute filters.
func attrMatches(a *afAttr, q map[string][]string) bool {
	if a.hidden && !strings.EqualFold(first(q, "showHidden"), "true") {
		return false
	}
	if a.excluded && !strings.EqualFold(first(q, "showExcluded"), "true") {
		return false
	}
	if !match(first(q, "nameFilter"), a.name) {
		return false
	}
	if c := first(q, "categoryName"); c != "" && !containsFold(a.cats, c) {
		return false
	}
	return true
}

// streamSetWrite answers POST streamsets/value (one value per stream)
// and streamsets/recorded (values per stream): 202 or 204 when every
// value was written, else 207 with each stream's substatus (and, for
// recorded, each value's).
func (s *PIServer) streamSetWrite(w http.ResponseWriter, what string, _ []*src, q map[string][]string, body []byte) {
	switch what {
	case "value":
		var list []struct {
			WebID string     `json:"WebId"`
			Value timedValue `json:"Value"`
		}
		if json.Unmarshal(body, &list) != nil {
			piError(w, 400, "The request body is not a list of stream values.")
			return
		}
		var items []any
		failed := 0
		for _, it := range list {
			sr := s.source(it.WebID)
			var err *readErr
			if sr == nil {
				err = &readErr{404, "The specified WebId is not valid or the object was not found."}
			} else {
				err = s.write(sr, it.Value, q)
			}
			sub, bad := substatus(err, q)
			sub["WebId"] = it.WebID
			items = append(items, sub)
			if bad {
				failed++
			}
		}
		s.multiStatus(w, q, items, failed)
	case "recorded":
		var list []struct {
			WebID string       `json:"WebId"`
			Items []timedValue `json:"Items"`
		}
		if json.Unmarshal(body, &list) != nil {
			piError(w, 400, "The request body is not a list of stream values.")
			return
		}
		var items []any
		failed := 0
		for _, it := range list {
			sr := s.source(it.WebID)
			if sr == nil {
				sub, _ := substatus(&readErr{404, "The specified WebId is not valid or the object was not found."}, q)
				sub["WebId"] = it.WebID
				items = append(items, sub)
				failed++
				continue
			}
			var subs []any
			bad := false
			for _, tv := range it.Items {
				sub, b := substatus(s.write(sr, tv, q), q)
				subs = append(subs, sub)
				bad = bad || b
			}
			if bad {
				failed++
			}
			if subs == nil {
				subs = []any{}
			}
			items = append(items, map[string]any{"WebId": it.WebID, "Items": subs})
		}
		s.multiStatus(w, q, items, failed)
	default:
		piError(w, 405, "The simulator does not support this method here.")
	}
}
