# Example: a date track

A MOQT endpoint serving a track of timestamps. Either side can publish or
subscribe, over native QUIC or WebTransport.

The track has one Object per second: the **Group ID is the Unix time**, and the
single Object in that Group is the second, formatted as RFC 3339. That makes it
deterministic, which is what lets the publisher answer a FETCH for seconds that
have already passed without keeping any history at all.

## Running it

```shell
git clone https://github.com/Eyevinn/moqtransport.git
cd moqtransport/examples/date
```

Publish from the server and subscribe from a client, in two shells:

```shell
go run . -server -publish
go run . -subscribe
```

Or the other way round — the roles are independent of which end listens:

```shell
go run . -server -subscribe
go run . -publish
```

The server always accepts WebTransport at `/moq` on the same port:

```shell
go run . -subscribe -webtransport -addr https://localhost:8080/moq
```

Without `-cert` and `-key` the server generates a throwaway certificate for
localhost, and clients skip verification.

## What each flag demonstrates

| Flag | What it shows |
|---|---|
| `-join 10` | Joining ten seconds behind the live edge, with the range worked out by the publisher so the join and the subscription meet exactly: a **fill** in draft-22, a **joining** FETCH in draft-18 |
| `-fetch 10` | A **standalone** FETCH: ten seconds named explicitly by the client, before subscribing |
| `-pause 5s` | A REQUEST_UPDATE turning delivery off and on again, the way a paused player would. The publisher stops sending and says so |
| Ctrl-C on the subscriber | The publisher noticing. There has been no UNSUBSCRIBE since draft-17: the subscriber resets its request stream, and the publisher's only sign is its subscription's context ending |
| `-trackname minute` | A rejection: the publisher answers DOES_NOT_EXIST rather than dropping the request |
| `-subscribe` on both ends | A nil handler answering NOT_SUPPORTED, which is a real answer rather than silence |
| `-draft 18` | Offering only one draft. By default a client offers `moqt-22` and `moqt-18`, and the server picks draft-22 |

```shell
# Subscribe, then fill four seconds behind the live edge.
go run . -subscribe -join 4

# Fetch four seconds explicitly, then subscribe.
go run . -subscribe -fetch 4

# Subscribe, pause after three seconds, resume three seconds later.
go run . -subscribe -pause 3s
```

### Fetching versus joining

Both retrieve Objects that already exist. The difference is who decides the
range, and it is the whole reason the joining forms exist.

`-fetch N` is a **standalone** FETCH: the client picks a range and asks for it.
It runs before the SUBSCRIBE, so where the range should stop is a guess — the
client does not learn where the live edge is until SUBSCRIBE_OK tells it, which
is after the fetch has already been sent. This one guesses "up to a second
ago", and is reliably one short:

```
fetched group 1788080296: 2026-08-30T08:58:16Z
fetched group 1788080297: 2026-08-30T08:58:17Z
fetched group 1788080298: 2026-08-30T08:58:18Z
subscribed, live edge is group 1788080299          <- never delivered
received group 1788080300: 2026-08-30T08:58:20Z
```

A better guess would narrow the gap, and against this particular track — a
clock, on a machine whose time agrees with the publisher's — the client could
even compute the right answer. Against a real track it cannot: only the
publisher knows where the edge is, and it keeps moving while the two requests
are in flight.

`-join N` asks the publisher for the range instead: how far back to go, with
the publisher working out the rest from the live edge it reports. The join and
the subscription are contiguous by construction — nothing missing at the join
point, and nothing delivered twice.

In draft-22 that is a **fill**, asked for in the SUBSCRIBE itself: the
subscription starts at the Next Object, and FILL_PARAMETERS asks for the Groups
from N back up to it, which arrive on a stream of their own.

```
session established, speaking moqt-22
subscribed, live edge is group 1792154385
filled  group 1792154381 object 0: 2026-10-16T12:39:41Z
filled  group 1792154382 object 0: 2026-10-16T12:39:42Z
filled  group 1792154383 object 0: 2026-10-16T12:39:43Z
filled  group 1792154384 object 0: 2026-10-16T12:39:44Z
filled  group 1792154385 object 0: 2026-10-16T12:39:45Z    <- the live edge itself
filled complete
received group 1792154386 object 0 (20 bytes): 2026-10-16T12:39:46Z   <- and straight on from it
```

In draft-18 (`-draft 18`) it is a **joining** FETCH, sent after the SUBSCRIBE
and naming it; the publisher resolves the range against that subscription's
Joining Location.

```
session established, speaking moqt-18
subscribed, live edge is group 1792154390
joining fetch accepted, ends at group 1792154390
joined  group 1792154386 object 0: 2026-10-16T12:39:46Z
...
joined  group 1792154390 object 0: 2026-10-16T12:39:50Z    <- the live edge itself
joined complete
received group 1792154391 object 0 (20 bytes): 2026-10-16T12:39:51Z   <- and straight on from it
```

That property is what the test asserts, on both drafts: the Group IDs across
the join and the subscription form one consecutive run.

The publisher serves all three the same way. A FETCH reaches `handleFetch` with
a range it resolves against its Largest Object, a joining FETCH with that range
already resolved against the subscription it names, and a fill reaches
`handleFill` resolved against the Largest Object the subscription reported;
both handlers hand the range to `writeSeconds`.

## How it talks

```mermaid
sequenceDiagram
    participant P as Publisher
    participant S as Subscriber

    P->>S: PUBLISH_NAMESPACE (clock)
    S->>P: REQUEST_OK

    opt -fetch (standalone)
        S->>P: FETCH (groups now-N .. now-1, chosen by the subscriber)
        P->>S: FETCH_OK
        P-->>S: FETCH_HEADER stream, N objects, FIN
    end

    S->>P: SUBSCRIBE (clock/second)
    P->>S: SUBSCRIBE_OK (LARGEST_OBJECT = the live edge)

    opt -join, draft-22
        Note over S,P: the SUBSCRIBE carried LOCATION_FILTER (Next Object) and FILL_PARAMETERS (N+1 groups back)
        P-->>S: FETCH_HEADER stream (SUBSCRIBE's Request ID), N+1 objects, FIN
    end

    opt -join, draft-18
        S->>P: FETCH (subscription's Request ID, N groups back)
        Note over P: range resolved from the subscription's Joining Location
        P->>S: FETCH_OK (End Location = live edge + 1)
        P-->>S: FETCH_HEADER stream, N+1 objects, FIN
    end

    loop every second
        P-->>S: SUBGROUP_HEADER stream, one object, FIN
    end

    opt -pause
        S->>P: REQUEST_UPDATE (FORWARD=0)
        P->>S: REQUEST_OK
        Note over P: stops opening subgroups
        S->>P: REQUEST_UPDATE (FORWARD=1)
        P->>S: REQUEST_OK
    end

    Note over S: Ctrl-C
    S--xP: reset the SUBSCRIBE stream
    Note over P: subscription context cancelled, publishing stops
```

Each announcement, subscription and fetch is a bidirectional stream of its own,
and Objects travel on unidirectional streams. Nothing is correlated by Request
ID at the API level: the stream is the request.

## Tests

`go test ./examples/date/` runs the example against itself over a real QUIC
stack, once per draft — both transports, a FETCH, the contiguity of a join
with its subscription, and a rejection. It is the only place
the transport adapters, the TLS ALPN and the WebTransport subprotocol are
exercised, so it takes a few seconds by construction: the track publishes once
a second.
