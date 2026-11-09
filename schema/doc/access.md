Which client addresses fit serves, by allow lists (whitelists) and deny lists (blacklists) of IP
addresses and CIDR prefixes, written inline or in files, as the lists published for firewalls are.
The top-level `access` is global: every listener, route, service and role goes by it. Each of them
may set an `access` of its own, which decides first, so that the more specific setting wins:

- a list's `deny` refuses an address, whatever its `allow` holds;
- else its `allow` serves an address it holds;
- else, when its `allow` has entries, it refuses the address, since an allow list serves only what
  it holds;
- else it has no say, and the next one out decides: a route's, then its listener's, then the global
  one; a service's, then its listener's, then the global one; a role's, then the global one. An
  address none has a say on is served.

A route's `allow` therefore serves an address the global `deny` refuses, and a route's `deny`
refuses one the global `allow` serves; `"allow": ["0.0.0.0/0", "::/0"]` opens a route or service
to every address whatever the global access says.

Where it can, fit decides when a connection is accepted: a refused TCP connection is closed before
anything is read from it, and the datagrams of a refused HTTP/3 or udp peer are dropped before a
session is made for them. A listener whose routes or services set an access of their own, or that
takes PROXY headers, decides later instead: a refused HTTP request is answered `403`, and a refused
`tcp` connection closed once its service is known. The address is the connection's, or the one its
PROXY header names, never `X-Forwarded-For`'s. A change applies to connections and udp sessions that
start after it.

The roles that listen on sockets of their own decide at accept too: `streaming` on its RTMP, RTSP
and SRT listeners and its WebRTC port, `webrtc.turn` on all three of its, and `webrtc.media` and
`webrtc.proxy` on their ports. A reload sets their policy again, the global access with it, without
starting a listener again.

The roles that listen, `tunnel.hub`, `tunnel.proxy`, `llm.hub`, `llm.node` and `llm.proxy`, each
take an `access` of their own for every socket the role listens on (a hub's listeners and
`console_socket`, a proxy's `forwards` and `web_proxies`, an LLM role's `listen` and a proxy's
`api`), refusing at accept; it and the global one are set again each time the config is applied,
without listening anew.

The Admin API (`admin.access`), the console (`admin.console.access`) and the roles' pages (the
`console.access` of `llm.*` and `tunnel.*`) go by the global access too, after their own, and
decide at accept: a refused connection is closed before anything is read from it. Unlike the rest of
`admin`, these follow every config applied, without a restart. A global `allow` that leaves out
`127.0.0.1` therefore shuts local clients out of the Admin API as well; to keep local access
whatever the global access says, set the API's own `"allow": ["127.0.0.1", "::1"]`, which decides
first. An Admin API on a Unix socket is never checked: its peers have no address, and setting
`admin.access` with one is an error. A config applied with an allow list that leaves out the address
the change came from does not close that connection, but the next one is refused.

A list file has one entry on each line, each line told apart by its content, so that one file may
mix them: an address or prefix, with what follows `#` or `;` left out; a range, `first-last`; an
eMule `ipfilter.dat` line, `first - last , level , name`, where a line of a level above 127 is not
listed (qBittorrent's reading, and eMule's at its default filter level); or a PeerGuardian `.p2p`
line, `name:first-last`. IPv4 addresses in ranges may have leading zeros, as `ipfilter.dat` writes
them. Blank lines and lines that begin with `#`, `;` or `//` are left out; any other line is an
error naming the file and the line, and the config is not applied. A file is read when the config
is applied, and the config applied again when it changes; each file is at most 64 MiB. The same
files serve every module with an `access`, the BitTorrent client's included.
An allow list on a listener that obtains certificates with ACME's HTTP-01 or TLS-ALPN-01 challenges
must serve the CA's validation servers, or the certificates cannot be obtained.

```json
{
  "access": {
    "deny": ["203.0.113.0/24"],
    "deny_files": ["/etc/fit/drop.txt"]
  },
  "listeners": [
    {"name": "web", "address": ":8080"},
    {"name": "internal", "address": ":8081", "access": {"allow": ["10.0.0.0/8", "192.168.0.0/16"]}}
  ],
  "routes": [
    {"listeners": ["web"], "match": {"path": ["/partner/*"]}, "access": {"allow": ["203.0.113.7", "198.51.100.0/24"]}, "handler": {"module": "respond", "params": {"body": "partners"}}},
    {"handler": {"module": "respond", "params": {"body": "hello"}}}
  ]
}
```
