# Changelog

All notable changes to this module. Versions follow semantic versioning;
before v1 the API may change in minor versions.

## v0.1.0 (2026-10-11)

First release as a module of its own, github.com/cB-Sigma/piwebapi-sim,
split from the piwebapi module; needs piwebapi v0.1.0.

- Kept: `sim.NewPIServer`, `NewUnstartedPIServer`, `PIServer` (`Base`,
  `AddPoint`, `Append`, `KerberosLogins`, `Keytab`, `Token`,
  `Requests`), `PIValue`; `sim/kdcsim`; `cmd/piwebapisim`.
- Asset Framework: `AddDatabase`, `AddElement` (`Element`,
  `AFAttribute` with PI point references or static values),
  `AddEventFrame` (`EventFrame`), and their lookups, lists and searches.
- Stream reads: value, end, recorded with every boundary type and
  filter expressions, interpolated with sync time, interpolated at
  times, plot, recorded at time and times, summary; PI time
  expressions.
- Writes with every update option and 202, 204 and 207 answers;
  read-only points (`SetReadOnly`); `Values`.
- Stream sets (ad hoc and of an element or event frame), stream
  updates, batch, `system/userinfo`, `system/versions`, `system/status`.
- Faults (`AddFault`, `ClearFaults`): any status, Retry-After, delays,
  per path and method; `MaxItems`; the CSRF check (`SetCSRFDefence`);
  `SetKeytab`; `Now` (the clock); `RequestLog`.
- `PIValue` gains `Text`, `Questionable`, `Substituted`, `Annotated`.
- Changed: WebIds are path-only WebID 2.0 WebIds (they were opaque
  strings); points whose values include a system state stay Float32
  unless a value has a non-system state; Basic login needs a user name.
- `piwebapisim` also serves an AF database with an element and an event
  frame.
- The piwebapi client's tests against the simulator and the Kerberos
  tests against the KDC moved here, and tests for every action were
  added.
