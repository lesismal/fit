// Package quic is the QUIC transport of the tunnel's links, on fib's own
// QUIC (github.com/lesismal/fib/http3/quic) and fib's engine: a listener is
// a UDP binding of the engine, and a dial is a UDP connection the engine
// dials, so that QUIC runs on the engine's event loops as every other
// connection of fit's does, with no socket or goroutine of its own.
//
// A link is one bidirectional stream of a QUIC connection of its own:
// quic://host:port. The opener of a stream sends one byte, streamVersion,
// before its data, which makes the stream known to the peer before the
// opener has anything else to say; the acceptor strips it. Nothing else is
// added: what one side writes is what the other reads. The TLS ALPN is
// "fit-tunnel" (ALPN).
//
// A dialer says how it checks the server's certificate in the address's
// query: pin=SHA-256 of the certificate (see Fingerprint), or insecure=1 to
// take any, for tests; without either, the system's roots check it. A
// listener presents the certificate transport.ListenOptions gives it; the
// tunnel's server keeps a self-signed one in its config.
//
// A connection's datagrams are handed to it on the engine's worker that
// read them, and its streams' data to the links on the same goroutine; a
// link's handlers do not block, so neither does QUIC. A stream's receive
// window is given back as the link takes the data, which it does at once:
// the tunnels' own windows pace what a link carries (see package tunnel).
//
// A client whose address changes mid-connection is not followed to its new
// address: its connection idles out, and it dials again.
package quic

// Scheme is the URL scheme of QUIC addresses.
const Scheme = "quic"

// ALPN is the TLS application protocol of the links.
const ALPN = "fit-tunnel"
