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

A list file has an address or prefix on each line, with blank lines and what follows `#` or `;`
left out; it is read when the config is applied, and the config applied again when it changes.
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
