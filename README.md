# Media over QUIC Transport (MoQT)

[![Go Reference](https://pkg.go.dev/badge/github.com/Eyevinn/moqtransport.svg)](https://pkg.go.dev/github.com/Eyevinn/moqtransport)

`moqtransport` is a Go implementation of [Media over QUIC Transport](https://datatracker.ietf.org/doc/draft-ietf-moq-transport/), on top of [quic-go](https://github.com/quic-go/quic-go) and optionally [webtransport-go](https://github.com/quic-go/webtransport-go/).

## Protocol version

The target is [draft-ietf-moq-transport-18](https://www.ietf.org/archive/id/draft-ietf-moq-transport-18.txt). A second declaration set for draft-20 is planned alongside it, sharing one architecture.

There is no version field on the wire from draft-17 onwards: SETUP does not carry one, and the version is settled out of band before a byte of MOQT is written. The ALPN string is therefore the whole interop contract — `moqt-18` for native QUIC, and the same identifier as the WebTransport subprotocol.

Drafts 14 and 16 are **not** supported. draft-17 changed the varint encoding, moved SETUP onto a pair of unidirectional streams, gave every request its own bidirectional stream, and reused codepoints across the break, so nothing of the older wire format survives. Use the `v0.10.x` line for draft-14 and draft-16.

## Status

The draft-18 rewrite is on the `draft-18` branch. The session and every request
type are implemented and tested end to end over an in-process transport:

- the `vi64` wire format, the message codecs and their generator
- the Message Parameter, Setup Option and Property registries, and subscription
  filters
- the SETUP control stream pair, per-request bidirectional streams, and the
  subgroup, datagram and FETCH data planes
- SUBSCRIBE, FETCH (including joining fetches), TRACK_STATUS,
  PUBLISH_NAMESPACE and SUBSCRIBE_NAMESPACE, in both directions

Not yet done: incoming PUBLISH and SUBSCRIBE_TRACKS, which are answered with
`NOT_SUPPORTED`; GOAWAY-driven session migration, which is read and ignored;
and the downstream move of `moqlivemock`.

## Design

Three things about draft-18 shape the API, and none of them is cosmetic.

**A request is an object with a lifetime.** The bidirectional stream *is* the identity of a request, so responses carry no Request ID. More to the point, `UNSUBSCRIBE`, `UNANNOUNCE`, `ANNOUNCE_CANCEL`, `UNSUBSCRIBE_ANNOUNCES` and `FETCH_CANCEL` are all gone: a peer that loses interest resets the stream and no message arrives at all. Every request therefore has a `Context()` that ends when the peer goes away, which is how a publisher learns it can stop publishing.

**Accepting a request returns the thing you publish on.** `Accept` answers with SUBSCRIBE_OK and hands back a `*Subscription`, rather than being both the response writer and the publisher. Writing Objects on a subscription the peer has not been told about is not representable.

**Requests carry their own updates.** `REQUEST_UPDATE` travels on the stream of the request it updates, so it belongs to the subscription rather than to the session.

The handler side of that:

```go
session.SubscribeHandler = moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
	if r.Track() != "video0" {
		r.Reject(moqtransport.RequestErrorDoesNotExist, "no such track")
		return
	}

	sub, err := r.Accept(moqtransport.WithLargestObject(largest))
	if err != nil {
		return
	}
	defer sub.Close(moqtransport.PublishDoneTrackEnded, "")

	for group := range groups {
		// The subscriber going away cancels the context; there is no
		// message to wait for.
		if sub.Context().Err() != nil {
			return
		}
		sg, err := sub.OpenSubgroup(group.ID, 0, 128, moqtransport.WithEndOfGroup())
		if err != nil {
			return
		}
		for i, object := range group.Objects {
			sg.WriteObject(uint64(i), object)
		}
		sg.Close()
	}
})
```

A nil handler rejects its request type with `NOT_SUPPORTED`, which is a legitimate answer and better than leaving the peer waiting.

## Usage

[`examples/date`](examples/date/README.md) is a working publisher and
subscriber for a track of timestamps, over both transports. It covers
announcements, subscriptions, fetches, subscription updates, and a subscriber
leaving.

```shell
cd examples/date
go run . -server -publish     # in one shell
go run . -subscribe -fetch 4  # in another
```

## Project structure

- `examples/date/`: a publisher and subscriber for a clock track
- `quicmoq/`: adapter for native QUIC connections
- `webtransportmoq/`: adapter for WebTransport sessions
- `internal/wire2/`: the draft-18 wire format — message codecs, the three Key-Value-Pair registries, and the hand-written data-plane bitfields
- `wiregen/`: the code generator that writes the message codecs from a declaration file

## Requirements

Go 1.25 or later. Dependencies are managed with Go modules.

## Origin

This project began as a fork of [github.com/mengelbart/moqtransport](https://github.com/mengelbart/moqtransport), taken over in March 2026 when development there had slowed. The draft-14 and draft-16 work, and the draft-18 rewrite that replaced the wire format and session layers outright, are Eyevinn's; little of the original code remains. The design of `wiregen` still derives from upstream's generator, so the original copyright is retained alongside Eyevinn's in [LICENSE](LICENSE).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT. See [LICENSE](LICENSE).
