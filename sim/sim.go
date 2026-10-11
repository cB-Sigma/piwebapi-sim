// Package sim is an in-process AVEVA PI Web API for tests, built from the
// public PI Web API reference (no shared code with the client): one Data
// Archive with points and archived values, and one AF server with
// databases, elements, attributes (PI point data references or static
// values) and event frames. It answers the stream and stream set reads
// (value, end, recorded, interpolated, interpolated at times, plot,
// summary, recorded at time and times), the value and values updates
// with every update option and 207 answers for partial failures, stream
// updates with markers, batch requests with parents and parameters, the
// object lookups by path, WebId, name and name filter with paging, and
// basic, bearer and Kerberos (HTTP Negotiate) logins. Kerberos tickets
// are checked by gokrb5's SPNEGO acceptor with a service keytab, for
// example one from the KDC in sim/kdcsim. Faults can be injected per path
// (any status, delays), points made read-only, and requests that change
// data are refused without X-Requested-With, as the PI Web API's CSRF
// defence does.
//
// WebIds are path-only WebID 2.0 WebIds, so a client can also make them
// from paths. Where the reference leaves a behaviour open (what a
// simulated summary computes, the status of a request whose batch parent
// failed), the package documentation of the function says what the
// simulator does.
package sim

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/gssapi"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/service"
	"github.com/jcmturner/gokrb5/v8/spnego"
)

// PIValue is one archived value of a PI point: a number (Value), text
// (Text, for a String point) or a digital state (State, with System for
// system states such as "Shutdown" or "I/O Timeout"), and its quality.
type PIValue struct {
	Time   time.Time
	Value  float64
	State  string
	System bool
	Bad    bool
	// Text is the value of a String point.
	Text string
	// Questionable, Substituted and Annotated are returned as the value's
	// flags of the same names.
	Questionable, Substituted, Annotated bool
}

// PIServer is a PI Web API with one Data Archive and one AF server. It
// follows the PI Web API reference: lists are {"Items": […]}, errors
// {"Errors": […]} with the status the reference gives (404 for an
// unknown object, 400 for a bad parameter, 401 for a refused login).
type PIServer struct {
	*httptest.Server
	Archive  string
	User     string
	Password string
	Token    string
	// Keytab, when set, also admits Kerberos: Authorization: Negotiate
	// with a SPNEGO token for a service whose key the keytab holds,
	// checked by gokrb5's acceptor as an IIS-hosted PI Web API checks it
	// against its service account. Challenges then offer Negotiate.
	Keytab *keytab.Keytab
	// KerberosUsers are the principals that logged in with Kerberos.
	KerberosUsers []string
	// AssetServerName is the AF server's name (default PIAF01).
	AssetServerName string
	// AllowNoCSRFHeader admits requests that change data without an
	// X-Requested-With header; by default they are refused with 403, as
	// the PI Web API's CSRF defence refuses them.
	AllowNoCSRFHeader bool
	// MaxItems is the most values or objects one read returns (the PI
	// Web API's MaxReturnedItemsPerCall; default 150000); a read asking
	// for more is refused with 400.
	MaxItems int
	// Now is the simulator's clock (default time.Now).
	Now func() time.Time

	// Delegation makes system/userinfo report the impersonation level
	// Delegation for Kerberos logins (else Impersonation).
	Delegation bool

	mu           sync.Mutex
	who, whoType string
	points       map[string]*piPoint // by name
	nextID       int
	Requests     []string
	faults       []*Fault
	dbs          []*afDB
	events       []updateEvent
	seq          int
}

type piPoint struct {
	name, units, desc, typ string
	id                     int
	values                 []PIValue
	readOnly               bool
	states                 []string // digital state names, by code
}

// NewPIServer starts a PI Web API at <URL>/piwebapi.
func NewPIServer(archive, user, password string) *PIServer {
	s := NewUnstartedPIServer(archive, user, password)
	s.Start()
	return s
}

// NewUnstartedPIServer is NewPIServer without starting the server, so
// that its Listener can be replaced (a fixed port) before Start.
func NewUnstartedPIServer(archive, user, password string) *PIServer {
	s := &PIServer{Archive: archive, User: user, Password: password, AssetServerName: "PIAF01", MaxItems: 150000,
		points: map[string]*piPoint{}}
	s.Server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	return s
}

// Base is the PI Web API's base URL.
func (s *PIServer) Base() string { return s.URL + "/piwebapi" }

func (s *PIServer) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// AddPoint creates a point with its archived values. Its type is Digital
// when a value has a State, String when one has Text, else Float32.
func (s *PIServer) AddPoint(name, units, desc string, values ...PIValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	typ := "Float32"
	for _, v := range values {
		switch {
		case v.State != "" && !v.System:
			typ = "Digital"
		case v.Text != "":
			typ = "String"
		}
	}
	vs := append([]PIValue(nil), values...)
	sortValues(vs)
	s.nextID++
	p := &piPoint{name: name, units: units, desc: desc, typ: typ, id: s.nextID, values: vs}
	for _, v := range vs {
		p.code(v)
	}
	s.points[name] = p
}

// Append archives more values of a point; stream updates report them.
func (s *PIServer) Append(name string, values ...PIValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.points[name]
	if p == nil {
		return
	}
	p.values = append(p.values, values...)
	sortValues(p.values)
	for _, v := range values {
		p.code(v)
		s.logEvent(s.pointWebID(p), v, "Add")
	}
}

// SetKeytab replaces the service keytab while the server runs (a service
// account whose key changed).
func (s *PIServer) SetKeytab(kt *keytab.Keytab) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Keytab = kt
}

// SetCSRFDefence turns the X-Requested-With check on or off while the
// server runs (AllowNoCSRFHeader is the opposite of on).
func (s *PIServer) SetCSRFDefence(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AllowNoCSRFHeader = !on
}

// SetMaxItems changes MaxItems while the server runs.
func (s *PIServer) SetMaxItems(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.MaxItems = n
}

// SetReadOnly makes writes to a point fail with 403, as a point the
// login may read but not write.
func (s *PIServer) SetReadOnly(name string, readOnly bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.points[name]; p != nil {
		p.readOnly = readOnly
	}
}

// Values returns a point's archived values (after writes).
func (s *PIServer) Values(name string) []PIValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.points[name]; p != nil {
		return append([]PIValue(nil), p.values...)
	}
	return nil
}

func sortValues(vs []PIValue) {
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].Time.Before(vs[j].Time) })
}

// code is a digital state's code in the point's state set (system states
// are not in it).
func (p *piPoint) code(v PIValue) int {
	if v.State == "" || v.System {
		return 0
	}
	for i, n := range p.states {
		if strings.EqualFold(n, v.State) {
			return i
		}
	}
	p.states = append(p.states, v.State)
	return len(p.states) - 1
}

// Fault makes matching requests fail or wait. A request matches when its
// method is Method (empty: any) and its path after /piwebapi/ starts with
// Path (empty: any). Delay is waited first (without holding up other
// requests); then a Status other than 0 is answered with Message in the
// Errors list (and Retry-After when set). Times limits the requests the
// fault applies to (0: until ClearFaults).
type Fault struct {
	Method     string
	Path       string
	Status     int
	Message    string
	Delay      time.Duration
	RetryAfter int
	Times      int
	used       int
}

// AddFault adds a fault.
func (s *PIServer) AddFault(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &f)
}

// ClearFaults removes every fault.
func (s *PIServer) ClearFaults() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = nil
}

// fault returns the first fault for r and counts its use.
func (s *PIServer) fault(method, path string) *Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.faultLocked(method, path)
}

// faultLocked is fault with s.mu held (a batch's sub-requests, which
// faults with a Status also reach; their Delay is not waited).
func (s *PIServer) faultLocked(method, path string) *Fault {
	for _, f := range s.faults {
		if (f.Method == "" || strings.EqualFold(f.Method, method)) && strings.HasPrefix(path, f.Path) && (f.Times == 0 || f.used < f.Times) {
			f.used++
			c := *f
			return &c
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func piError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"Errors": []string{msg}})
}

func (s *PIServer) serve(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/piwebapi")
	path = strings.TrimPrefix(path, "/")
	if f := s.fault(r.Method, path); f != nil {
		if f.Delay > 0 {
			select {
			case <-time.After(f.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if f.Status != 0 {
			if f.RetryAfter > 0 {
				w.Header().Set("Retry-After", itoa(f.RetryAfter))
			}
			msg := f.Message
			if msg == "" {
				msg = http.StatusText(f.Status)
			}
			piError(w, f.Status, msg)
			return
		}
	}
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, 32<<20))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, r.URL.Path+"?"+r.URL.RawQuery)
	authed := false
	if s.Token != "" && r.Header.Get("Authorization") == "Bearer "+s.Token {
		authed = true
	}
	if u, p, ok := r.BasicAuth(); ok && s.User != "" && u == s.User && p == s.Password {
		authed = true
	}
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Negotiate "); ok && s.Keytab != nil {
		if who, ok := s.negotiate(tok); ok {
			authed = true
			s.KerberosUsers = append(s.KerberosUsers, who)
		}
	}
	s.who, s.whoType = "", ""
	switch {
	case strings.HasPrefix(r.Header.Get("Authorization"), "Negotiate ") && authed:
		s.who, s.whoType = s.KerberosUsers[len(s.KerberosUsers)-1], "Kerberos"
	case strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") && authed:
		s.who, s.whoType = "bearer", "Bearer"
	case authed:
		s.who, s.whoType = s.User, "Basic"
	}
	if !authed {
		if s.Keytab != nil {
			w.Header().Add("WWW-Authenticate", "Negotiate")
		}
		w.Header().Add("WWW-Authenticate", `Basic realm="PI Web API"`)
		piError(w, http.StatusUnauthorized, "Authorization has been denied for this request.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !s.AllowNoCSRFHeader &&
		r.Header.Get("X-Requested-With") == "" {
		piError(w, http.StatusForbidden, "This request needs an X-Requested-With header (CSRF defence).")
		return
	}
	if len(strings.TrimSpace(string(body))) > 0 && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		piError(w, http.StatusUnsupportedMediaType, "The request body must be application/json.")
		return
	}
	s.route(w, r.Method, path, r.URL.Query(), body, 0)
}

// route answers one request (also a batch's sub-request); depth limits
// nested batches.
func (s *PIServer) route(w http.ResponseWriter, method, path string, q map[string][]string, body []byte, depth int) {
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	get := method == http.MethodGet
	switch {
	case path == "" && get:
		writeJSON(w, 200, map[string]any{"Links": map[string]string{"Self": s.Base() + "/", "AssetServers": s.Base() + "/assetservers",
			"DataServers": s.Base() + "/dataservers", "System": s.Base() + "/system"}})
	case path == "system" && get:
		writeJSON(w, 200, map[string]any{"ProductTitle": "PI Web API simulator", "ProductVersion": "1.0.0"})
	case path == "system/userinfo" && get:
		writeJSON(w, 200, s.userInfo())
	case path == "system/versions" && get:
		writeJSON(w, 200, map[string]any{"OSIsoft.REST.Core": map[string]any{"FullVersion": "1.0.0", "MajorMinorRevision": "1.0.0", "Build": "0"}})
	case path == "system/status" && get:
		writeJSON(w, 200, map[string]any{"UpTimeInMinutes": 1.5, "State": "Running", "CacheInstances": 0, "ServerTime": s.now().Format(time.RFC3339Nano)})
	case path == "batch" && method == http.MethodPost:
		if depth > 0 {
			piError(w, 400, "A batch cannot hold a batch.")
			return
		}
		s.batch(w, body, depth)
	case parts[0] == "dataservers" || parts[0] == "points":
		s.routePI(w, method, parts, q)
	case parts[0] == "streams" || parts[0] == "streamsets":
		s.routeStreams(w, method, parts, q, body)
	case parts[0] == "assetservers" || parts[0] == "assetdatabases" || parts[0] == "elements" || parts[0] == "attributes" || parts[0] == "eventframes":
		s.routeAF(w, method, parts, q, body)
	default:
		piError(w, 404, "The requested resource does not exist.")
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// negotiate checks a SPNEGO token with the service keytab and returns the
// client principal.
func (s *PIServer) negotiate(b64 string) (string, bool) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}
	var st spnego.SPNEGOToken
	if err := st.Unmarshal(b); err != nil {
		return "", false
	}
	ok, ctx, status := spnego.SPNEGOService(s.Keytab, service.DecodePAC(false)).AcceptSecContext(&st)
	if !ok || status.Code != gssapi.StatusComplete {
		return "", false
	}
	id, _ := ctx.Value("github.com/jcmturner/gokrb5/v8/ctxCredentials").(*credentials.Credentials) // gokrb5 keeps the identity under this key
	if id == nil {
		return "?", true
	}
	return id.UserName() + "@" + id.Domain(), true
}

// userInfo describes the caller of the request being served. The
// identity types and levels are the simulator's: WindowsIdentity for
// Basic and Kerberos logins, ClaimsIdentity for a bearer token.
func (s *PIServer) userInfo() map[string]any {
	typ, level := "WindowsIdentity", "Impersonation"
	if s.whoType == "Bearer" {
		typ, level = "ClaimsIdentity", "None"
	}
	if s.whoType == "Kerberos" && s.Delegation {
		level = "Delegation"
	}
	return map[string]any{"IdentityType": typ, "Name": s.who, "IsAuthenticated": s.who != "", "SID": "S-1-5-21-0-0-0-1001", "ImpersonationLevel": level}
}

// KerberosLogins returns the principals that logged in with Kerberos.
func (s *PIServer) KerberosLogins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.KerberosUsers...)
}

// RequestLog returns the requests served so far (path?query).
func (s *PIServer) RequestLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Requests...)
}

// webID is the path-only WebID 2.0 WebId of the object at path: "P1",
// the kind's marker, the owner's marker (attributes), and the path
// without its leading \\, upper case, in URL-safe base64 without padding.
func webID(marker, owner, path string) string {
	return "P1" + marker + owner + base64.RawURLEncoding.EncodeToString([]byte(strings.ToUpper(strings.TrimPrefix(path, `\\`))))
}

// match is a PI name filter: * any run, ? one character, case-insensitive;
// empty matches everything.
func match(filter, name string) bool {
	if filter == "" {
		return true
	}
	f, n := []rune(strings.ToLower(filter)), []rune(strings.ToLower(name))
	var m func(i, j int) bool
	m = func(i, j int) bool {
		for i < len(f) {
			switch f[i] {
			case '*':
				for k := j; k <= len(n); k++ {
					if m(i+1, k) {
						return true
					}
				}
				return false
			case '?':
				if j >= len(n) {
					return false
				}
			default:
				if j >= len(n) || n[j] != f[i] {
					return false
				}
			}
			i++
			j++
		}
		return j == len(n)
	}
	return m(0, 0)
}
