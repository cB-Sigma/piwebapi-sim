# Provenance

## How the code was written

Written for SigmaM2M, a oneM2M platform, in Go, by the author of the
piwebapi module. No code was ported or copied from another
implementation.

The PI Web API simulator (`sim`) began as SigmaM2M's historian test
server, and the KDC (`sim/kdcsim`) as the test KDC of SigmaM2M's
Kerberos package. Both were written from the references below, not
from the client, and became part of the piwebapi module on 2026-10-06.
On 2026-10-11 they, the `piwebapisim` command and the tests that start
them moved into this module, so that the library does not depend on its
simulator.

For the first release (v0.1.0) the simulator was extended to every
action the piwebapi client calls: the Asset Framework and event frames,
every stream and stream set read, writes with update options and 207
answers, stream updates, batch, `system/userinfo`, the CSRF check,
faults and the item cap. Where the reference leaves a behaviour open,
the simulator's choice is written in its package documentation: the
summary arithmetic, the 409 of a batch request whose parent failed,
the reading of each event frame search mode, the system state codes.

The KDC uses gokrb5's message types and RFC 3961 profiles; its exchange
logic (pre-authentication, the TGS checks, the errors) is this module's
own, so a client is tested against a KDC's decisions rather than
against its own code.

## Sources

| Area (files) | Followed | Read for facts only (no code taken) |
|---|---|---|
| PI Web API simulator (sim/) | AVEVA PI Web API reference (docs.aveva.com/bundle/pi-web-api-reference): controllers, actions, parameters and defaults, answer shapes, errors, substatus, batch, stream updates, the CSRF defence, WebID 2.0 | OSIsoft's generated PI Web API client for Python (Apache-2.0): action paths, parameter and field names, WebID markers |
| Test KDC (sim/kdcsim) | RFC 4120 sections 3.1, 3.3 (AS and TGS exchanges) and 7.2 (TCP and UDP framing), RFC 3962 (AES), PA-ENC-TIMESTAMP and ETYPE-INFO2 pre-authentication | gokrb5's message types |
| Command (cmd/piwebapisim) | none (flags are our own) | |

No specification or vendor text is reproduced.

## Tests

| Test | Source |
|---|---|
| reads_test.go, streams_test.go, sets_test.go, objects_test.go, example_test.go, plant_test.go | the piwebapi client against this simulator, written from the same references by the same author; not a real PI Web API |
| negotiate/kerberos_test.go | gokrb5's client (through piwebapi's negotiate) against this KDC and gokrb5's acceptor |
| sim/, cmd/piwebapisim | the simulator and the command on their own |

All names, values, realms, passwords and keys are made-up demo values.
