An HTTP CONNECT proxy for the clients `clients` lists, to the destinations `allow` and `deny`
permit; a listener's `socks` does the same for SOCKS5.

Whatever `allow` holds, loopback, link-local (`169.254.169.254`, the metadata service of most clouds,
among them), unspecified and multicast addresses, and the addresses of the host fit runs on, are
refused unless `allow` names them: a client would otherwise reach the Admin API on `127.0.0.1:2019`
and every other service the host keeps to itself. A host name is decided by the address it was
connected to, before anything is sent. `allow` names such an address with a range inside its own,
`127.0.0.0/8` or `169.254.169.254` say, not with a wider one such as `0.0.0.0/0`, and names one of the
host's own addresses by writing that address alone. To reach them as well as everything else, list
the wide ranges too: `"allow": ["0.0.0.0/0", "::/0", "127.0.0.0/8"]`.

```json route
{
  "match": {"method": ["CONNECT"]},
  "handler": {"module": "forward_proxy", "params": {
    "clients": ["10.0.0.0/8"],
    "allow": ["*.example.com"],
    "ports": [443]
  }}
}
```
