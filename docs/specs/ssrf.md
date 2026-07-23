# Outbound fetch guard

`internal/safety` owns every request this server makes to an address someone
else chose: feeds, avatars, images, domain verification, webmention delivery.
Nothing else may construct an `http.Client` for remote work.

## The invariant

**A destination is validated after DNS resolution and immediately before the
connection is made, on every hop.**

Validating the URL is not enough. `http://feeds.example.org/` can resolve to a
public address when checked and to `127.0.0.1` a moment later — DNS rebinding.
The check therefore lives in `net.Dialer.Control`, which the runtime calls with
the already-resolved `IP:port` right before `connect(2)`. There is no window
between the check and the connection for the answer to change.

Redirects get the same treatment for free: each hop dials, so each hop is
validated. `CheckURL` additionally runs per hop to keep the scheme honest.

## Blocked destinations

`CheckAddr` rejects, with a reason attached to the error:

IPv4 — `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`,
`169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`,
`192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`,
`224.0.0.0/4`, `240.0.0.0/4`.

IPv6 — `::/128`, `::1/128`, `64:ff9b::/96`, `64:ff9b:1::/48`, `100::/64`,
`2001::/32`, `2001:db8::/32`, `2002::/16`, `fc00::/7`, `fe80::/10`, `ff00::/8`.

Also rejected: any IPv4-mapped IPv6 address (`::ffff:0:0/96`), outright rather
than by unmapping, because the mapped form is a filter-bypass technique and no
legitimate resolver hands us one; and any address carrying a zone.

`169.254.169.254` deserves the specific mention: it is the cloud metadata
endpoint, and reaching it is how an SSRF becomes a credential leak.
`64:ff9b::/96`, `2002::/16` and `2001::/32` are blocked because NAT64, 6to4 and
Teredo each embed an IPv4 address that would otherwise reach a private network
through an IPv6 destination.

## Other constraints

- Schemes: `http` and `https` only.
- URLs carrying userinfo are rejected. `http://api.trusted.org@evil.example/`
  is a phishing and SSRF vector, never a real feed.
- At most 3 redirects.
- 5 MB response ceiling. Oversize is an error, not a truncation — a truncated
  feed is not a smaller feed, it is a corrupt one.
- Dial 10 s, response header 20 s, whole request 30 s.
- `Authorization` and `Cookie` are stripped from the initial request and again
  on every redirect. The client has no cookie jar.
- **`Transport.Proxy` is nil, deliberately.** An egress proxy would receive the
  target URL and do its own resolution, which puts the actual connection
  outside `Dialer.Control` and voids the invariant above. Honouring
  `HTTP_PROXY` would silently disable this guard.

## The escape hatch

`Options.AllowPrivateAddrs` disables address checking. It exists for two
legitimate uses: the local development server, and the integration test where
two instances federate over loopback. It must never be reachable from instance
configuration that a non-owner can edit, and it never disables the scheme,
credential, redirect, or size checks.

## Caller obligations

Callers log the final URL from `Result.URL` and, on failure, the error — which
carries the blocked address and the reason. The guard does not log; it does not
know which subsystem is asking or what the operator wants recorded.
