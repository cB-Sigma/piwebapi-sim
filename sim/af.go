// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AFAttribute is an attribute of an element or event frame: a PI point
// data reference (Point names a point of the Data Archive) or, without
// Point, a static value (Static: a number, text or a boolean).
type AFAttribute struct {
	Name, Description, Units string
	// Type is the value type (Double, Int32, String, Boolean...); empty
	// takes it from the point or the static value.
	Type       string
	Point      string
	Static     any
	Categories []string
	// Hidden and Excluded attributes are left out of lists and stream
	// sets unless showHidden or showExcluded asks for them.
	Hidden, Excluded bool
	// Step marks the values as steps (no interpolation).
	Step     bool
	Children []AFAttribute
}

// Element is an AF element to add: Path is the database and the element
// names below it ("Plant\Area1\Boiler1"); missing parents are added as
// plain elements.
type Element struct {
	Path        string
	Template    string
	Description string
	Categories  []string
	Attributes  []AFAttribute
}

// EventFrame is an AF event frame to add, in Database, under the event
// frame named Parent when set. A zero End is an event frame in progress.
// Elements are the referenced elements' paths below the database
// ("Area1\Boiler1").
type EventFrame struct {
	Database, Name, Parent       string
	Template, Description        string
	Severity                     string
	Start, End                   time.Time
	Elements                     []string
	Categories                   []string
	Attributes                   []AFAttribute
	Acknowledged, CanAcknowledge bool
}

type afDB struct {
	name, desc string
	roots      []*afElement
	efs        []*afEF
}

type afElement struct {
	name, desc, template string
	cats                 []string
	db                   *afDB
	parent               *afElement
	children             []*afElement
	attrs                []*afAttr
	server               string
}

type afEF struct {
	name, desc, template, severity string
	start, end                     time.Time
	cats                           []string
	db                             *afDB
	parent                         *afEF
	children                       []*afEF
	attrs                          []*afAttr
	elements                       []*afElement
	ack, canAck                    bool
	server                         string
}

type afAttr struct {
	name, desc, units, typ, point string
	static                        any
	staticTime                    time.Time
	cats                          []string
	hidden, excluded, step        bool
	children                      []*afAttr
	parent                        *afAttr
	ownerPath, ownerMarker        string
}

func (s *PIServer) afPath() string { return `\\` + s.AssetServerName }

func (d *afDB) path(server string) string { return `\\` + server + `\` + d.name }

func (e *afElement) path() string {
	if e.parent != nil {
		return e.parent.path() + `\` + e.name
	}
	return e.db.path(e.server) + `\` + e.name
}

func (e *afEF) path() string {
	if e.parent != nil {
		return e.parent.path() + `\EventFrames[` + e.name + `]`
	}
	return e.db.path(e.server) + `\EventFrames[` + e.name + `]`
}

func (a *afAttr) path() string {
	if a.parent != nil {
		return a.parent.path() + "|" + a.name
	}
	return a.ownerPath + "|" + a.name
}

func (a *afAttr) webID() string { return webID("Ab", a.ownerMarker, a.path()) }

func (s *PIServer) staticJSON(a *afAttr, units string) map[string]any {
	t := a.staticTime
	if t.IsZero() {
		t = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return map[string]any{"Timestamp": t.UTC().Format(time.RFC3339Nano), "Value": a.static, "UnitsAbbreviation": units,
		"Good": true, "Questionable": false, "Substituted": false, "Annotated": false}
}

// guid is a stable made-up GUID for an object path.
func guid(path string) string {
	h := fnv.New128a()
	_, _ = h.Write([]byte(strings.ToUpper(path)))
	b := h.Sum(nil)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// AddDatabase adds an AF database.
func (s *PIServer) AddDatabase(name, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db(name) == nil {
		s.dbs = append(s.dbs, &afDB{name: name, desc: description})
	}
}

func (s *PIServer) db(name string) *afDB {
	for _, d := range s.dbs {
		if strings.EqualFold(d.name, name) {
			return d
		}
	}
	return nil
}

func (s *PIServer) newAttrs(list []AFAttribute, ownerPath, marker string, parent *afAttr) []*afAttr {
	var out []*afAttr
	for _, a := range list {
		at := &afAttr{name: a.Name, desc: a.Description, units: a.Units, typ: a.Type, point: a.Point, static: a.Static, cats: a.Categories,
			hidden: a.Hidden, excluded: a.Excluded, step: a.Step, parent: parent, ownerPath: ownerPath, ownerMarker: marker}
		at.children = s.newAttrs(a.Children, ownerPath, marker, at)
		out = append(out, at)
	}
	return out
}

// AddElement adds an element with its attributes.
func (s *PIServer) AddElement(e Element) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := strings.Split(strings.Trim(e.Path, `\`), `\`)
	if len(names) < 2 {
		return errors.New("sim: an element path is the database and at least one element name")
	}
	d := s.db(names[0])
	if d == nil {
		return fmt.Errorf("sim: no AF database %s", names[0])
	}
	var parent *afElement
	list := &d.roots
	var el *afElement
	for _, n := range names[1:] {
		el = nil
		for _, c := range *list {
			if strings.EqualFold(c.name, n) {
				el = c
			}
		}
		if el == nil {
			el = &afElement{name: n, db: d, parent: parent, server: s.AssetServerName}
			*list = append(*list, el)
		}
		parent, list = el, &el.children
	}
	el.desc, el.template, el.cats = e.Description, e.Template, e.Categories
	el.attrs = append(el.attrs, s.newAttrs(e.Attributes, el.path(), "E", nil)...)
	return nil
}

// AddEventFrame adds an event frame.
func (s *PIServer) AddEventFrame(ef EventFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.db(ef.Database)
	if d == nil {
		return fmt.Errorf("sim: no AF database %s", ef.Database)
	}
	n := &afEF{name: ef.Name, desc: ef.Description, template: ef.Template, severity: ef.Severity, start: ef.Start.UTC(), end: ef.End.UTC(),
		cats: ef.Categories, db: d, ack: ef.Acknowledged, canAck: ef.CanAcknowledge, server: s.AssetServerName}
	if ef.End.IsZero() {
		n.end = time.Time{}
	}
	for _, p := range ef.Elements {
		el := s.elementByPath(d.path(s.AssetServerName) + `\` + strings.Trim(p, `\`))
		if el == nil {
			return fmt.Errorf("sim: no element %s in %s", p, d.name)
		}
		n.elements = append(n.elements, el)
	}
	if ef.Parent != "" {
		var found *afEF
		s.eachEF(func(e *afEF) {
			if e.db == d && strings.EqualFold(e.name, ef.Parent) {
				found = e
			}
		})
		if found == nil {
			return fmt.Errorf("sim: no event frame %s", ef.Parent)
		}
		n.parent = found
		found.children = append(found.children, n)
	} else {
		d.efs = append(d.efs, n)
	}
	n.attrs = s.newAttrs(ef.Attributes, n.path(), "F", nil)
	return nil
}

func (s *PIServer) eachElement(fn func(*afElement)) {
	var walk func([]*afElement)
	walk = func(list []*afElement) {
		for _, e := range list {
			fn(e)
			walk(e.children)
		}
	}
	for _, d := range s.dbs {
		walk(d.roots)
	}
}

func (s *PIServer) eachEF(fn func(*afEF)) {
	var walk func([]*afEF)
	walk = func(list []*afEF) {
		for _, e := range list {
			fn(e)
			walk(e.children)
		}
	}
	for _, d := range s.dbs {
		walk(d.efs)
	}
}

func (s *PIServer) eachAttr(fn func(*afAttr)) {
	var walk func([]*afAttr)
	walk = func(list []*afAttr) {
		for _, a := range list {
			fn(a)
			walk(a.children)
		}
	}
	s.eachElement(func(e *afElement) { walk(e.attrs) })
	s.eachEF(func(e *afEF) { walk(e.attrs) })
}

func (s *PIServer) elementByPath(path string) *afElement {
	var found *afElement
	s.eachElement(func(e *afElement) {
		if strings.EqualFold(e.path(), path) {
			found = e
		}
	})
	return found
}

func (s *PIServer) elementByWebID(id string) *afElement {
	var found *afElement
	s.eachElement(func(e *afElement) {
		if webID("Em", "", e.path()) == id {
			found = e
		}
	})
	return found
}

func (s *PIServer) efByWebID(id string) *afEF {
	var found *afEF
	s.eachEF(func(e *afEF) {
		if webID("Fm", "", e.path()) == id {
			found = e
		}
	})
	return found
}

func (s *PIServer) efByPath(path string) *afEF {
	var found *afEF
	s.eachEF(func(e *afEF) {
		if strings.EqualFold(e.path(), path) {
			found = e
		}
	})
	return found
}

func (s *PIServer) attrByWebID(id string) *afAttr {
	var found *afAttr
	s.eachAttr(func(a *afAttr) {
		if a.webID() == id {
			found = a
		}
	})
	return found
}

func (s *PIServer) attrByPath(path string) *afAttr {
	var found *afAttr
	s.eachAttr(func(a *afAttr) {
		if strings.EqualFold(a.path(), path) {
			found = a
		}
	})
	return found
}

func (s *PIServer) dbByWebID(id string) *afDB {
	for _, d := range s.dbs {
		if webID("RD", "", d.path(s.AssetServerName)) == id {
			return d
		}
	}
	return nil
}

// attrsOf returns the attributes of an element or event frame WebId.
func (s *PIServer) attrsOf(id string) ([]*afAttr, bool) {
	if e := s.elementByWebID(id); e != nil {
		return e.attrs, true
	}
	if e := s.efByWebID(id); e != nil {
		return e.attrs, true
	}
	return nil, false
}

func (s *PIServer) assetServerJSON() map[string]any {
	return map[string]any{"WebId": webID("RS", "", s.afPath()), "Id": guid(s.afPath()), "Name": s.AssetServerName, "Description": "",
		"Path": s.afPath(), "IsConnected": true, "ServerVersion": "2.10.11.2717", "ExtendedProperties": map[string]any{}}
}

func (s *PIServer) dbJSON(d *afDB) map[string]any {
	p := d.path(s.AssetServerName)
	return map[string]any{"WebId": webID("RD", "", p), "Id": guid(p), "Name": d.name, "Description": d.desc, "Path": p, "ExtendedProperties": map[string]any{}}
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func (s *PIServer) elementJSON(e *afElement) map[string]any {
	p := e.path()
	return map[string]any{"WebId": webID("Em", "", p), "Id": guid(p), "Name": e.name, "Description": e.desc, "Path": p,
		"TemplateName": e.template, "HasChildren": len(e.children) > 0, "CategoryNames": nonNil(e.cats), "ExtendedProperties": map[string]any{}}
}

func (s *PIServer) attrJSON(a *afAttr) map[string]any {
	p := a.path()
	typ, dr, cfg := a.typ, "", ""
	if a.point != "" {
		dr, cfg = "PI Point", `\\`+s.Archive+`\`+a.point
		if typ == "" {
			typ = "Double"
			if pt := s.points[a.point]; pt != nil {
				switch pt.typ {
				case "Digital":
					typ = "EnumerationValue"
				case "String":
					typ = "String"
				}
			}
		}
	}
	if typ == "" {
		switch a.static.(type) {
		case string:
			typ = "String"
		case bool:
			typ = "Boolean"
		case int, int32, int64:
			typ = "Int32"
		default:
			typ = "Double"
		}
	}
	return map[string]any{"WebId": a.webID(), "Id": guid(p), "Name": a.name, "Description": a.desc, "Path": p, "Type": typ, "TypeQualifier": "",
		"DefaultUnitsName": a.units, "DataReferencePlugIn": dr, "ConfigString": cfg, "IsConfigurationItem": a.point == "",
		"IsExcluded": a.excluded, "IsHidden": a.hidden, "IsManualDataEntry": false, "HasChildren": len(a.children) > 0,
		"CategoryNames": nonNil(a.cats), "Step": a.step}
}

var inProgressEnd = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

func (s *PIServer) efJSON(e *afEF) map[string]any {
	p := e.path()
	end := e.end
	if end.IsZero() {
		end = inProgressEnd
	}
	refs := []string{}
	for _, el := range e.elements {
		refs = append(refs, webID("Em", "", el.path()))
	}
	return map[string]any{"WebId": webID("Fm", "", p), "Id": guid(p), "Name": e.name, "Description": e.desc, "Path": p,
		"TemplateName": e.template, "HasChildren": len(e.children) > 0, "CategoryNames": nonNil(e.cats), "ExtendedProperties": map[string]any{},
		"StartTime": e.start.Format(time.RFC3339Nano), "EndTime": end.Format(time.RFC3339Nano), "Severity": e.severity,
		"CanBeAcknowledged": e.canAck, "IsAcknowledged": e.ack, "IsAnnotated": false, "IsLocked": false, "AreValuesCaptured": !e.end.IsZero(),
		"RefElementWebIds": refs}
}

func notFound(w http.ResponseWriter) {
	piError(w, 404, "The specified WebId is not valid or the object was not found.")
}

func (s *PIServer) routeAF(w http.ResponseWriter, method string, parts []string, q map[string][]string, body []byte) {
	get := method == http.MethodGet
	kind := parts[0]
	if len(parts) == 2 && parts[1] == "multiple" && get {
		switch kind {
		case "elements":
			s.multiple(w, q, func(p string) (any, string) {
				if e := s.elementByPath(p); e != nil {
					return s.elementJSON(e), ""
				}
				return nil, "Element not found '" + p + "'."
			}, func(id string) (any, string) {
				if e := s.elementByWebID(id); e != nil {
					return s.elementJSON(e), ""
				}
				return nil, "The specified WebId is not valid or the object was not found."
			})
			return
		case "attributes":
			s.multiple(w, q, func(p string) (any, string) {
				if a := s.attrByPath(p); a != nil {
					return s.attrJSON(a), ""
				}
				return nil, "Attribute not found '" + p + "'."
			}, func(id string) (any, string) {
				if a := s.attrByWebID(id); a != nil {
					return s.attrJSON(a), ""
				}
				return nil, "The specified WebId is not valid or the object was not found."
			})
			return
		}
	}
	if len(parts) == 1 {
		if !get {
			piError(w, 405, "The simulator does not change AF configuration.")
			return
		}
		name, path := first(q, "name"), first(q, "path")
		switch kind {
		case "assetservers":
			switch {
			case name == "" && path == "":
				writeJSON(w, 200, map[string]any{"Items": []any{s.assetServerJSON()}})
			case strings.EqualFold(name, s.AssetServerName) || strings.EqualFold(path, s.afPath()):
				writeJSON(w, 200, s.assetServerJSON())
			default:
				piError(w, 404, "PI AF server not found.")
			}
		case "assetdatabases":
			for _, d := range s.dbs {
				if strings.EqualFold(d.path(s.AssetServerName), path) {
					writeJSON(w, 200, s.dbJSON(d))
					return
				}
			}
			piError(w, 404, "AF database not found for path '"+path+"'.")
		case "elements":
			if e := s.elementByPath(path); e != nil {
				writeJSON(w, 200, s.elementJSON(e))
				return
			}
			piError(w, 404, "Element not found for path '"+path+"'.")
		case "attributes":
			if a := s.attrByPath(path); a != nil {
				writeJSON(w, 200, s.attrJSON(a))
				return
			}
			piError(w, 404, "Attribute not found for path '"+path+"'.")
		case "eventframes":
			if e := s.efByPath(path); e != nil {
				writeJSON(w, 200, s.efJSON(e))
				return
			}
			piError(w, 404, "Event frame not found for path '"+path+"'.")
		}
		return
	}
	id := parts[1]
	sub := ""
	if len(parts) > 2 {
		sub = strings.Join(parts[2:], "/")
	}
	if !get && !(kind == "attributes" && sub == "value" && method == http.MethodPut) {
		piError(w, 405, "The simulator does not change AF configuration.")
		return
	}
	switch kind {
	case "assetservers":
		if id != webID("RS", "", s.afPath()) {
			notFound(w)
			return
		}
		switch sub {
		case "":
			writeJSON(w, 200, s.assetServerJSON())
		case "assetdatabases":
			items := []any{}
			for _, d := range s.dbs {
				items = append(items, s.dbJSON(d))
			}
			writeJSON(w, 200, map[string]any{"Items": items})
		default:
			piError(w, 404, "The requested resource does not exist.")
		}
	case "assetdatabases":
		d := s.dbByWebID(id)
		if d == nil {
			notFound(w)
			return
		}
		switch sub {
		case "":
			writeJSON(w, 200, s.dbJSON(d))
		case "elements":
			s.elementList(w, d.roots, q)
		case "eventframes":
			s.efList(w, d.efs, q, nil)
		default:
			piError(w, 404, "The requested resource does not exist.")
		}
	case "elements":
		e := s.elementByWebID(id)
		if e == nil {
			notFound(w)
			return
		}
		switch sub {
		case "":
			writeJSON(w, 200, s.elementJSON(e))
		case "elements":
			s.elementList(w, e.children, q)
		case "attributes":
			s.attrList(w, e.attrs, q)
		case "eventframes":
			s.efList(w, e.db.efs, q, e)
		default:
			piError(w, 404, "The requested resource does not exist.")
		}
	case "attributes":
		a := s.attrByWebID(id)
		if a == nil {
			notFound(w)
			return
		}
		switch sub {
		case "":
			writeJSON(w, 200, s.attrJSON(a))
		case "attributes":
			s.attrList(w, a.children, q)
		case "value":
			sr := s.attrSrc(a)
			if method == http.MethodPut {
				if a.point != "" {
					piError(w, 400, "The attribute has a data reference; write its value through streams/{webId}/value.")
					return
				}
				var tv timedValue
				if json.Unmarshal(body, &tv) != nil {
					piError(w, 400, "The request body is not a timed value.")
					return
				}
				if err := s.write(sr, tv, q); err != nil {
					piError(w, err.code, err.msg)
					return
				}
				writeJSON(w, http.StatusNoContent, nil)
				return
			}
			res, err := s.read(sr, "value", map[string][]string{})
			if err != nil {
				piError(w, err.code, err.msg)
				return
			}
			writeJSON(w, 200, res)
		default:
			piError(w, 404, "The requested resource does not exist.")
		}
	case "eventframes":
		e := s.efByWebID(id)
		if e == nil {
			notFound(w)
			return
		}
		switch sub {
		case "":
			writeJSON(w, 200, s.efJSON(e))
		case "attributes":
			s.attrList(w, e.attrs, q)
		case "eventframes":
			s.efList(w, e.children, q, nil)
		case "referencedelements":
			items := []any{}
			for _, el := range e.elements {
				items = append(items, s.elementJSON(el))
			}
			writeJSON(w, 200, map[string]any{"Items": items})
		default:
			piError(w, 404, "The requested resource does not exist.")
		}
	}
}

func sortByName(items []any, q map[string][]string) {
	desc := strings.EqualFold(first(q, "sortOrder"), "Descending")
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].(map[string]any)["Name"].(string), items[j].(map[string]any)["Name"].(string)
		if desc {
			return strings.ToLower(a) > strings.ToLower(b)
		}
		return strings.ToLower(a) < strings.ToLower(b)
	})
}

func (s *PIServer) elementList(w http.ResponseWriter, roots []*afElement, q map[string][]string) {
	full := strings.EqualFold(first(q, "searchFullHierarchy"), "true")
	items := []any{}
	var walk func([]*afElement)
	walk = func(list []*afElement) {
		for _, e := range list {
			ok := match(first(q, "nameFilter"), e.name) && match(first(q, "descriptionFilter"), e.desc)
			if c := first(q, "categoryName"); c != "" && !containsFold(e.cats, c) {
				ok = false
			}
			if t := first(q, "templateName"); t != "" && !strings.EqualFold(t, e.template) {
				ok = false
			}
			if ok {
				items = append(items, s.elementJSON(e))
			}
			if full {
				walk(e.children)
			}
		}
	}
	walk(roots)
	sortByName(items, q)
	if items, ok := s.page(w, q, items); ok {
		writeJSON(w, 200, map[string]any{"Items": items})
	}
}

func (s *PIServer) attrList(w http.ResponseWriter, attrs []*afAttr, q map[string][]string) {
	full := strings.EqualFold(first(q, "searchFullHierarchy"), "true")
	items := []any{}
	var walk func([]*afAttr)
	walk = func(list []*afAttr) {
		for _, a := range list {
			ok := attrMatches(a, q)
			if vt := first(q, "valueType"); vt != "" && ok {
				ok = strings.EqualFold(s.attrJSON(a)["Type"].(string), vt)
			}
			if ok {
				items = append(items, s.attrJSON(a))
			}
			if full {
				walk(a.children)
			}
		}
	}
	walk(attrs)
	if items, ok := s.page(w, q, items); ok {
		writeJSON(w, 200, map[string]any{"Items": items})
	}
}

// efList searches event frames (and, with searchFullHierarchy, their
// children); ref, when set, keeps those that reference that element. The
// search modes are read as: Overlapped (default) the frames that overlap
// [startTime, endTime]; Inclusive those inside it; StartInclusive those
// that start in it; EndInclusive those that end in it; Forward/
// BackwardFromStartTime those that start after/before startTime;
// Forward/BackwardFromEndTime those that end after/before startTime;
// Forward/BackwardInProgress those in progress that started after/before
// startTime. Backward modes list the latest first.
func (s *PIServer) efList(w http.ResponseWriter, roots []*afEF, q map[string][]string, ref *afElement) {
	now := s.now()
	start, ok1 := s.parsePITime(first(q, "startTime"), now.Add(-8*time.Hour))
	end, ok2 := s.parsePITime(first(q, "endTime"), now)
	if !ok1 || !ok2 {
		piError(w, 400, "Cannot parse the start or end time.")
		return
	}
	mode := strings.ToLower(first(q, "searchMode"))
	full := strings.EqualFold(first(q, "searchFullHierarchy"), "true")
	var found []*afEF
	var walk func([]*afEF)
	walk = func(list []*afEF) {
		for _, e := range list {
			found = append(found, e)
			if full {
				walk(e.children)
			}
		}
	}
	walk(roots)
	var keep []*afEF
	for _, e := range found {
		eEnd := e.end
		inProgress := eEnd.IsZero()
		if inProgress {
			eEnd = inProgressEnd
		}
		var in bool
		switch mode {
		case "", "overlapped":
			in = !e.start.After(end) && !eEnd.Before(start)
		case "inclusive":
			in = !e.start.Before(start) && !eEnd.After(end)
		case "startinclusive":
			in = !e.start.Before(start) && !e.start.After(end)
		case "endinclusive":
			in = !inProgress && !eEnd.Before(start) && !eEnd.After(end)
		case "forwardfromstarttime":
			in = !e.start.Before(start)
		case "backwardfromstarttime":
			in = !e.start.After(start)
		case "forwardfromendtime":
			in = !inProgress && !eEnd.Before(start)
		case "backwardfromendtime":
			in = !inProgress && !eEnd.After(start)
		case "forwardinprogress":
			in = inProgress && !e.start.Before(start)
		case "backwardinprogress":
			in = inProgress && !e.start.After(start)
		default:
			piError(w, 400, "Unknown searchMode '"+first(q, "searchMode")+"'.")
			return
		}
		if !in || !match(first(q, "nameFilter"), e.name) {
			continue
		}
		if t := first(q, "templateName"); t != "" && !strings.EqualFold(t, e.template) {
			continue
		}
		if c := first(q, "categoryName"); c != "" && !containsFold(e.cats, c) {
			continue
		}
		if sev := all(q, "severity"); len(sev) > 0 && !containsFold(sev, e.severity) {
			continue
		}
		if v := first(q, "isAcknowledged"); v != "" && strings.EqualFold(v, "true") != e.ack {
			continue
		}
		if v := first(q, "canBeAcknowledged"); v != "" && strings.EqualFold(v, "true") != e.canAck {
			continue
		}
		refOK := ref == nil
		refName, refTemplate := first(q, "referencedElementNameFilter"), first(q, "referencedElementTemplateName")
		matched := refName == "" && refTemplate == ""
		for _, el := range e.elements {
			if el == ref {
				refOK = true
			}
			if match(refName, el.name) && (refTemplate == "" || strings.EqualFold(refTemplate, el.template)) {
				matched = true
			}
		}
		if refOK && matched {
			keep = append(keep, e)
		}
	}
	desc := strings.HasPrefix(mode, "backward") || strings.EqualFold(first(q, "sortOrder"), "Descending")
	sort.SliceStable(keep, func(i, j int) bool {
		if desc {
			return keep[i].start.After(keep[j].start)
		}
		return keep[i].start.Before(keep[j].start)
	})
	items := make([]any, len(keep))
	for i, e := range keep {
		items[i] = s.efJSON(e)
	}
	if items, ok := s.page(w, q, items); ok {
		writeJSON(w, 200, map[string]any{"Items": items})
	}
}
