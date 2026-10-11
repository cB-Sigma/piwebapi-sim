# piwebapi-sim

A simulated AVEVA PI Web API and a test Kerberos KDC, in Go, for testing PI Web API clients without a PI System; not affiliated with AVEVA.

[![Go Reference](https://pkg.go.dev/badge/github.com/cB-Sigma/piwebapi-sim.svg)](https://pkg.go.dev/github.com/cB-Sigma/piwebapi-sim) [![test](https://github.com/cB-Sigma/piwebapi-sim/actions/workflows/test.yml/badge.svg)](https://github.com/cB-Sigma/piwebapi-sim/actions/workflows/test.yml) [![Licence: Apache-2.0](https://img.shields.io/badge/licence-Apache--2.0-blue.svg)](LICENSE) [![Go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.mod)

The simulator is an HTTP server (in-process, through `httptest`) with
one Data Archive of points and archived values and one AF server with
databases, elements, attributes and event frames. It answers every
action the [piwebapi](https://github.com/cB-Sigma/piwebapi) client
calls, can be made to fail in the ways a real server fails, and checks
Kerberos tickets with gokrb5's acceptor. The KDC issues those tickets:
it runs the AS and TGS exchanges of RFC 4120 with pre-authentication,
on TCP and UDP. Written for SigmaM2M, a oneM2M platform; it has no
dependency on it.

## What is simulated

| Feature | Status |
|---|---|
| Logins: Basic, Bearer, Kerberos (HTTP Negotiate checked with a service keytab, `SetKeytab` to change it while running); 401 with `WWW-Authenticate` challenges; `system/userinfo` naming the caller | yes |
| CSRF defence: POST, PUT, PATCH, DELETE without `X-Requested-With` refused with 403 (`SetCSRFDefence`); a body that is not JSON refused with 415 | yes |
| Data Archive: list, by name, by path, by WebId; points by path, WebId, name filter (`*`, `?`) with startIndex and maxCount, `points/multiple`, point attributes | yes |
| Asset Framework: asset server, databases, elements (filters, full hierarchy, paging, sort), attributes (PI point data references and static values, child attributes, hidden and excluded, categories, value type), `elements/multiple`, `attributes/multiple`, attribute value GET and PUT | yes |
| Event frames: every search mode, time range, name, referenced element (name and template), template, category, severity, acknowledgement, children, attributes, referenced elements; in progress shown with an end in 9999 | yes |
| Stream reads: value (snapshot, at a time), end, recorded (Inside, Outside, Interpolated boundaries; maxCount; backwards when start is after end), interpolated (interval, sync time), interpolated at times, plot, recorded at time and times (every retrieval mode), summary | yes |
| PI times: absolute ISO 8601 and `*`, `t`, `y` with `+`/`-` spans | yes |
| Filter expressions of the form `'tag' > 50` (`>`, `>=`, `<`, `<=`, `=`, `<>`), with filtered values marked when asked | yes; other expressions answer 400 |
| Writes: update value and values, every update option (Replace, Insert, NoReplace, ReplaceOnly, InsertNoCompression, Remove), 202 or 204 by buffer option, numbers, text, digital states by name or code, system states; 207 with each item's substatus | yes |
| Stream sets: ad hoc and of an element or event frame, every read; per-stream failures as `WebException` items; value and values updates with 207 per stream and per value | yes |
| Stream updates: register and read by marker, for one stream and for a set; events for appended, written, replaced and removed values | yes |
| Batch: parents, JSONPath parameters, request templates, repeated query parameters, 409 for a request whose parent failed, 207 when one failed; cycles and unknown parents refused | yes |
| Quality: bad, questionable, substituted, annotated values; system states (Shutdown, I/O Timeout, No Data, Filtered) | yes |
| Faults: any status (401, 403, 404, 409, 5xx with Retry-After...) on chosen paths and methods, a number of times or until cleared; slow answers; read-only points (403 on write); unknown WebIds (404); `MaxItems` (400 above it) | yes |
| WebIds: path-only WebID 2.0, so a client can build them from paths | yes |
| Channels (WebSockets), calculations, indexed search, configuration writes | no |
| A real server's numbers: summaries are the simulator's arithmetic (time-weighted by trapezoids, the PI per-day total convention), system state codes are its own, `selectedFields` and `webIdType` are accepted but not applied | no |

## Install

```
go get github.com/cB-Sigma/piwebapi-sim@latest
```

Requires Go 1.26 or later. The command:

```
go install github.com/cB-Sigma/piwebapi-sim/cmd/piwebapisim@latest
```

## Command line

```
piwebapisim -addr 127.0.0.1:8080 -user demo -password demo-pw
piwebapisim -realm DEMO.EXAMPLE    # also a KDC; prints its krb5.conf
```

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `127.0.0.1:0` | listen address |
| `-archive` | `PISRV01` | Data Archive name |
| `-user` | `demo` | Basic (and, with `-realm`, Kerberos) user |
| `-password` | `demo-pw` | the user's password |
| `-token` | | a bearer token also accepted |
| `-realm` | | start a KDC for this realm and accept Kerberos |

It serves three points with a value a minute for the last hour
(BLR1.STEAM.TEMP, BLR1.FEED.FLOW, BLR1.PUMP.STATE), the AF server PIAF01
with database Demo, element Boiler1 whose attributes refer to them, and
an event frame in progress.

## In Go tests

From `Example` in example_test.go:

```go
s := sim.NewPIServer("PISRV01", "demo", "demo-pw")
defer s.Close()
s.AddPoint("BLR1.STEAM.TEMP", "degC", "steam temperature",
	sim.PIValue{Time: base, Value: 181.5}, sim.PIValue{Time: base.Add(time.Minute), Value: 182})
s.AddDatabase("Plant", "")
_ = s.AddElement(sim.Element{Path: `Plant\Boiler1`, Attributes: []sim.AFAttribute{
	{Name: "Steam Temperature", Point: "BLR1.STEAM.TEMP"}, {Name: "Rated", Static: 190.0, Units: "degC"}}})
s.AddFault(sim.Fault{Path: "streams/", Status: 503, RetryAfter: 2, Times: 1})

c := piwebapi.New(s.Base(), nil, piwebapi.Basic{User: "demo", Password: "demo-pw"})
```

With Kerberos:

```go
kdc, _ := kdcsim.Start("PLANT.EXAMPLE")
_ = kdc.AddUser("svc", "demo-password")
s.Keytab, _ = kdc.AddService(negotiate.SPNFor(s.Base()), "demo-service-password")
k, _ := negotiate.New(negotiate.Config{Realm: "PLANT.EXAMPLE", KDCs: []string{kdc.Addr}, Username: "svc", Password: "demo-password"})
c := piwebapi.New(s.Base(), nil, k.For(negotiate.SPNFor(s.Base())))
```

## Documentation

Reference: https://pkg.go.dev/github.com/cB-Sigma/piwebapi-sim

| Package | What it does |
|---|---|
| `sim` | `NewPIServer`, `NewUnstartedPIServer`, `PIServer` (`AddPoint`, `Append`, `Values`, `SetReadOnly`, `AddDatabase`, `AddElement`, `AddEventFrame`, `AddFault`, `ClearFaults`, `SetKeytab`, `SetCSRFDefence`, `SetMaxItems`, `KerberosLogins`, `RequestLog`, `Now`), `PIValue`, `Element`, `AFAttribute`, `EventFrame`, `Fault` |
| `sim/kdcsim` | `Start` a KDC for a realm on 127.0.0.1 (TCP and UDP); `AddUser`, `UserKeytab`, `AddService` (returns the service keytab), `Krb5Conf`, `Exchanges` |
| `cmd/piwebapisim` | Serves the simulator with demo points, AF objects and an event frame, and with `-realm` a KDC as well |

Changes per version are in [CHANGELOG.md](CHANGELOG.md).

## Testing

```
go test -race ./...
```

The tests at the module root drive the piwebapi client against the
simulator: every stream and stream set action, writes with every update
option and partial failures, stream updates, batch, the object lookups,
event frame searches, faults, paging, CSRF. `negotiate/` logs in with
the piwebapi negotiate package against the KDC: password and keytab,
wrong password, unknown user and service, a blocked KDC, and a service
key change that makes the client log in again.

### Interop tested against

- Not yet: a real PI Web API, Active Directory or an IIS-hosted service.
  None was available. The simulator's answers follow AVEVA's public PI
  Web API reference and the public client generated from its 2018 API
  description; where the reference leaves a behaviour open, the
  package documentation says what the simulator does.
- The simulator and the piwebapi client have the same author, so their
  agreement is not independent evidence.
- The KDC's messages and cryptography are gokrb5's, and the client side
  is gokrb5 too: the tests show that gokrb5's client works against
  exchanges written from RFC 4120, not that either matches Active
  Directory.

## Demo values

Every server name, point, value, realm, user name, password, token and
key in this module is a made-up demo value. The KDC's keys exist only
in memory for the life of a test. Do not use them anywhere else.

## Related repositories

- [piwebapi](https://github.com/cB-Sigma/piwebapi): the PI Web API
  client this module is built for.

## Status

v0.x: the API may change before v1.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Security

See [SECURITY.md](SECURITY.md).

## Licence

Apache-2.0 (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). Copyright 2026
Abhinav Gautam. AVEVA, OSIsoft, PI System and PI Web API are trademarks
of their owners; the names are used only to say which product this
module simulates.
