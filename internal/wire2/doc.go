// Package wire2 implements the MOQT wire format from draft-18 onwards.
//
// It replaces internal/wire, which speaks drafts 14 and 16. The two cannot be
// merged: draft-17 replaced RFC 9000 varints with the leading-ones vi64
// encoding, moved SETUP onto a pair of unidirectional streams, gave every
// request its own bidirectional stream, and reused codepoints across the
// break (0x5 is SUBSCRIBE_ERROR in draft-16 and REQUEST_ERROR here). Once the
// session layer speaks draft-18, internal/wire is deleted and this package
// takes its name.
//
// Message codecs are generated from the declaration file by ../../wiregen.
// Each declaration set targets one draft and generates methods suffixed with
// that draft (appendV18/parseV18), so several drafts share one architecture:
// a build offers every ALPN it has a declaration set for and lets negotiation
// pick. Only the primitives that the generator cannot express are written by
// hand: Location, Key-Value-Pair, the subgroup and datagram bitfields, and
// the fetch object serialization flags.
package wire2
