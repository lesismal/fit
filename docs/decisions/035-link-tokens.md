# 035: Issued tokens for the tunnel and the LLM mesh

[English](035-link-tokens.md) | [简体中文](035-link-tokens.zh-CN.md)

Status: accepted (2026-11-06). Builds on [decision 028](028-llm-and-tunnel.md) (the roles) and the
TLS of the tunnel's and the mesh's links, decided the same day (I151–I154).

## Context

The tunnel and the LLM mesh authenticated their links with a password every member shared: the
tunnel hub's `password` (and `body_password`), and an LLM listener's `password` or its role
passwords. The security review of 2026-11-06 found three consequences:

- **A member could pose as another.** Any node that held a role's password could say hello under
  another node's name or ID and receive its share of the calls, prompts included; and, holding the
  listener's password, pose as the listener to the other members of the role.
- **A peer that answered a dial could guess the password offline.** The LLM mesh's dialer proved
  the password with an HMAC over a challenge the listener chose; the tunnel's body keys came from
  PBKDF2 at 10,000 rounds under a salt every install shares. Either is enough to test guesses at a
  human-chosen password.
- **The secret was long-lived and could not be revoked for one member** without changing it on
  every member.

The links are TLS or QUIC off loopback now, which keeps an attacker on the path out; what was left
is the shared, guessable, everlasting secret. The iteration is I159.

## Decisions

- **Tokens a listener issues.** A token is `fitt1.<id>.<secret>`: a 16-hex-digit ID and a 32-byte
  random secret in unpadded base64url. The listener keeps a record of it in its member (`tunnel.hub`'s
  `tokens`, an LLM role's `tokens`): the ID, a name, the creation time, an optional expiry, an
  optional note, and a hash of the secret, never the secret. The token is shown once, when it is made.
- **Not bound to a role.** A valid token is taken from a node, a proxy or a hub alike: what a link may
  do still follows from the role it says hello as and the listener's rules for that role
  (`allow_hub_manage`, `allow_hub_services`, `allow_proxy_services`). A role-bound token would have
  added a second kind of rule beside the ones that already decide this.
- **A member is bound to its first token.** The first time a member registers (the tunnel: its
  `node/<name>` or `proxy/<name>`) or says hello (the mesh: its ID) with a token, the listener binds
  it to that token and writes the binding into the token's `members`; another token is refused for
  that member from then on, across restarts. Revoking the token frees its members: moving a member
  to a new token is revoking its old one first.
- **What the listener keeps cannot link.** From the secret: `client = HMAC-SHA256(secret, label)`,
  and the hash kept is `stored = SHA-256(client)`. The dialer proves the token with
  `client XOR HMAC-SHA256(stored, "dialer to listener" | transcript)`; the listener recovers `client`
  and checks its SHA-256 against `stored`, as SCRAM does. So a copy of the config, or of the store's
  plaintext, does not let anyone link as a member. The listener's own proof and the body keys derive
  from `stored` by HKDF, so the listener needs nothing more than the hash; whoever holds the hash can
  pose as the listener, which they could anyway with the listener's certificate and key.
- **The handshake.** Both sides add a fresh 32-byte nonce: in the mesh, the listener's challenge and
  the dialer's nonce after it; in the tunnel, a new `challenge` call before the registration, carrying
  the token's ID and the dialer's nonce, answered with the hub's. The transcript is a label naming
  the protocol, the token's ID, both nonces, and in the tunnel the peer's type and name, each after its
  length. The dialer's proof names the direction dialer-to-listener, the listener's
  listener-to-dialer and covers its answer (the tunnel's registration answer), under a key of its own
  (`server = HKDF(stored, label)`). So a proof sent back to its sender, replayed on another link, or
  answered to another nonce does not check. The secret has 256 bits: an observed proof leaks nothing
  worth guessing.
- **Body keys by HKDF.** A tunnel link with `body_password` that registers with a token says so in its
  hello (version 4, the token's ID after its random bytes), and both sides key it with
  `HKDF(body_key(stored) | body_password, both hellos' random bytes, direction)`: no PBKDF2, no shared
  salt. The hub refuses a registration whose token is not the hello's. Links with the password keep
  version 3's PBKDF2 keys, so that earlier nodes still link during the upgrade.
- **Lifetimes.** A token expires at a set time or after a duration (`30d`, `720h`); an expired token is
  refused at the next handshake, and the listener closes the links it holds at the expiry, checking at
  least once a minute (MaxSweep) should the wall clock jump. A change of a token's expiry, or its
  revocation, is a config edit: applying it closes the links of a token removed, expired or whose hash
  changed at once.
- **Management in three places, one code path.** Package `internal/linktoken` holds the tokens'
  crypto, the listener's registry of tokens and links, and a manager that issues, lists, changes and
  revokes them through a config edit of the role's member. The role's pages (the tunnel hub's Tokens
  page, the LLM roles' links page), the Admin API (`/tokens`, `/tokens/{role}`, `/tokens/{role}/{id}`)
  and `fit token list|create|expiry|revoke` all call that manager. Each change is logged to the `admin`
  logger. A token's last use, the address it came from and the links it holds are runtime state, not
  kept: a restart forgets them until the members link again.
- **The shared password, for one release.** The tunnel hub's `password` and the LLM listener's
  passwords still authenticate, so that members can be moved one at a time. The hub's status and the
  mesh's links list who still uses one, and the pages warn while anyone does; the settings let the
  password be removed once nobody does. The next release refuses it. A dialer has `token` or
  `password`, never both; a hub, or an LLM listener, may have neither, and then takes tokens alone.

## Rejected

- **Role-bound tokens.** What a link may do is the role's rules; a token's role would duplicate them and
  not stop a node from posing as another node, which the member binding does.
- **Keeping the token itself, or a plain hash used as the HMAC key.** Either makes a copy of the config
  a credential to link with. The SCRAM-style proof keeps what the listener stores useless to a dialer.
- **Public-key credentials (a key pair per member).** Stronger against a stolen config, but every member
  would need a key and a way to enroll it; the issued-token model fits the pages and the CLI that make
  tokens today, and keeps the handshake inside the frames the links already speak.
- **Tokens only, at once.** It would break every tunnel and mesh on upgrade; one release of overlap,
  with a warning, lets members move one at a time.
- **Persisting last use.** A config edit on each handshake would churn the config's history and store
  for state nobody configures.
