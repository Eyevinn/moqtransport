// Command date is a MOQT publisher and subscriber for a track of timestamps.
//
// The track has one Object per second: the Group ID is the Unix time and the
// single Object in it is that second, formatted. That makes the track
// deterministic, which is what lets the same program answer a FETCH for
// seconds that have already passed without keeping any history.
//
// Either side can publish or subscribe, over native QUIC or WebTransport.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

const appName = "date"

const usage = `%s is a MOQT endpoint serving a track of timestamps, one Object
per second. It can act as server or client, and can publish, subscribe, or
both.

Usage of %s:
`

type options struct {
	certFile     string
	keyFile      string
	addr         string
	server       bool
	publish      bool
	subscribe    bool
	fetch        uint
	join         uint
	pause        time.Duration
	webtransport bool
	namespace    string
	trackname    string
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usage, appName, appName)
		fmt.Fprintf(os.Stderr, "%s [options]\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.StringVar(&opts.certFile, "cert", "localhost.pem", "TLS certificate file (server only)")
	fs.StringVar(&opts.keyFile, "key", "localhost-key.pem", "TLS key file (server only)")
	fs.StringVar(&opts.addr, "addr", "localhost:8080", "listen or connect address")
	fs.BoolVar(&opts.server, "server", false, "run as server")
	fs.BoolVar(&opts.publish, "publish", false, "publish the date track")
	fs.BoolVar(&opts.subscribe, "subscribe", false, "subscribe to the date track")
	fs.UintVar(&opts.fetch, "fetch", 0, "standalone FETCH: this many seconds of history, before subscribing")
	fs.UintVar(&opts.join, "join", 0, "joining FETCH: this many seconds behind the subscription, after subscribing")
	fs.DurationVar(&opts.pause, "pause", 0, "after this long, pause delivery for the same again (subscriber only)")
	fs.BoolVar(&opts.webtransport, "webtransport", false, "use WebTransport instead of native QUIC (client only)")
	fs.StringVar(&opts.namespace, "namespace", "clock", "namespace to publish or subscribe to")
	fs.StringVar(&opts.trackname, "trackname", "second", "track to publish or subscribe to")
	err := fs.Parse(args[1:])
	return &opts, err
}

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	opts, err := parseOptions(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if !opts.publish && !opts.subscribe {
		fs.Usage()
		return errors.New("nothing to do: pass -publish, -subscribe, or both")
	}
	if opts.fetch > 0 && !opts.subscribe {
		return errors.New("-fetch needs -subscribe")
	}
	if opts.join > 0 && !opts.subscribe {
		return errors.New("-join needs -subscribe")
	}
	if opts.pause > 0 && !opts.subscribe {
		return errors.New("-pause needs -subscribe")
	}

	// Ctrl-C ends the session, which cancels every request on it. That is the
	// whole shutdown path: publishers stop because their subscription's
	// context is done, not because anything tells them to.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	h := &moqHandler{
		namespace: []string{opts.namespace},
		trackname: opts.trackname,
		publish:   opts.publish,
		subscribe: opts.subscribe,
		fetch:     uint64(opts.fetch),
		join:      uint64(opts.join),
		pause:     opts.pause,
	}
	if opts.server {
		tlsConfig, err := serverTLSConfig(opts.certFile, opts.keyFile)
		if err != nil {
			return err
		}
		return h.runServer(ctx, opts.addr, tlsConfig)
	}
	return h.runClient(ctx, opts.addr, opts.webtransport)
}

// serverTLSConfig loads the given certificate, falling back to a throwaway one
// for localhost.
//
// The protocol list is what makes one port serve both transports: a MOQT
// version for native QUIC, and h3 for the WebTransport upgrade.
func serverTLSConfig(certFile, keyFile string) (*tls.Config, error) {
	nextProtos := append(moqtransport.SupportedALPNs(), "h3")

	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err == nil {
			return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: nextProtos}, nil
		}
		log.Printf("no usable certificate in %v/%v (%v), generating one for localhost",
			certFile, keyFile, err)
	}

	generated, err := selfSignedCert()
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{generated}, NextProtos: nextProtos}, nil
}

// selfSignedCert makes a short-lived certificate for localhost. Clients in
// this example skip verification, so it exists only to have the handshake
// complete.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
}

// dialQUIC connects over native QUIC, where the MOQT version is the TLS ALPN.
func dialQUIC(ctx context.Context, addr string) (moqtransport.Connection, error) {
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         moqtransport.SupportedALPNs(),
	}, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		return nil, err
	}
	return quicmoq.NewClient(conn), nil
}

// dialWebTransport connects over WebTransport, where the MOQT version is the
// negotiated subprotocol rather than the TLS ALPN. Offering it is not
// optional: from draft-17 there is no version field in SETUP to fall back on,
// so a session whose subprotocol is not a MOQT version cannot start.
func dialWebTransport(ctx context.Context, addr string) (moqtransport.Connection, error) {
	dialer := webtransport.Transport{
		TLSClientConfig:      &tls.Config{InsecureSkipVerify: true},
		ApplicationProtocols: moqtransport.SupportedALPNs(),
	}
	_, session, err := dialer.Dial(ctx, addr, nil)
	if err != nil {
		return nil, err
	}
	return webtransportmoq.NewClient(session), nil
}
