# 0103. A satellite the inventory does not pin is found on the network

- **Status:** accepted · refines [ADR-0017](0017-monorepo-polyglot-by-seam.md) and [ADR-0066](0066-the-device-proves-which-satellite-it-is-before-any-audio.md)
- **Source:** SPEC §3.2.2, §13 · ADR-0017, ADR-0020, ADR-0066, ADR-0074

ADR-0017 split the inventory by volatility: mDNS (`_esphomelib._tcp`)
would discover addresses, and static YAML would carry the keys, rooms and
profiles. Discovery was never built. `chorusd` refused any address that was
not an IP literal, so a household had to give every satellite a static
lease and copy it into `devices.yaml`, and SPEC §13 described a feature
that did not exist.

An inventory address may now be pinned or found. An IP literal pins it, as
before. A hostname is resolved: a `.local` name over mDNS, any other by the
system resolver. With no address at all, the satellite is discovered over
mDNS as its node name's `_esphomelib._tcp` instance, the advertisement
every ESPHome node already makes, and the instance's SRV gives the native
API's port.

```yaml
satellites:
  - name: kitchen-satellite1-a1b2c3   # found as kitchen-satellite1-a1b2c3._esphomelib._tcp.local
    psk: ...
    room: kitchen
  - name: office-voice-pe-5c7e91
    address: office-voice-pe-5c7e91.local:6053
    psk: ...
    room: office
```

## How it is built

`internal/mdns` sends one-shot queries (RFC 6762 §5.1): one question, ID
zero, multicast to 224.0.0.251:5353 from an ephemeral port with an IP TTL of
255. A query from a port other than 5353 is legacy unicast, which a
responder answers to its sender (§6.7), so `chorusd` neither binds 5353 nor
joins the group, and runs beside Avahi or anything else that does. A query
is asked three times, waiting one, two, then four seconds, on the injected
timers. A reply counts only if it is a response, its record names the
question, its class is IN with the cache-flush bit ignored, and its TTL is
not zero; TTL zero is a goodbye (§10.1). When the instance's reply leaves
out the host's address, the host the SRV names is asked for.

`internal/discover` turns an inventory entry into an address: mDNS, or the
system resolver for a name not under `.local`. `cmd/probe` uses it too, so
it dials a satellite where `chorusd` would.

In `chorusd`, each satellite that is not pinned gets a loop that finds it
when the daemon starts, again before the answer lapses, and with backoff
from five seconds to a minute while nobody answers. The lapse is the
answer's TTL held between one and five minutes: a legacy unicast answer
may say ten seconds (§6.7), and asking that often buys nothing that a
nudge does not. A nudge has a satellite looked for at once. A link from an
address that matches no satellite, or one that proves another satellite's
name, nudges every satellite that is found; a native API that will not
dial nudges its own. A satellite moved by its DHCP lease therefore has its
first link from the new address refused, and the next one, five seconds
later at the device's own redial, heard.

The native API is not dialled until the satellite is first found. After
that the last address found is kept while nobody answers, since the device
must still prove its key to use it.

## A found address is where a link is taken from, not who it is

ADR-0066 made a satellite its name, its key and its address together.
Discovery changes where the address comes from, not the rule: the device
must still dial from the address the inventory pins, or discovery last
found, for the name it proves. Two satellites found at one address match
nobody until one of them is found elsewhere, with a warning naming both;
pinned, the same clash is still refused at startup.

mDNS is unauthenticated. Anyone on the link can answer for a satellite's
name. What that buys is limited: a spoofed answer can have links from the
real satellite refused, a denial of service that anyone able to send to
the link can already cause by cutting the connection (ADR-0074), or have a
link taken from the spoofer's own address, which then fails the key proof
both ways. The native API is the same: a dial to a spoofed address fails
the Noise handshake under the satellite's PSK. The address check was
defence in depth on top of the key, and it stays that.

## Alternatives rejected

**Browse `_esphomelib._tcp` and offer every node found.** An ESPHome node
that is not in the inventory has no PSK there, so nothing could talk to
it. Asking for each inventory name directly is one query per satellite and
learns nothing a household did not write down.

**Bind 5353 and keep a cache of announcements.** That hears a satellite
move without being asked, but it fights Avahi or systemd-resolved for the
port on the hosts a household runs `chorusd` on. A satellite's own link
says when it has moved, which is when it matters.

**Trust the key alone and drop the address check for found satellites.**
That weakens ADR-0066 for every household that adopts discovery, to save
one refused link per move.

**cgo and nss-mdns.** It needs libc and Avahi in the image, which is
distroless and static, and it hides the retries and TTLs in a library that
cannot be driven by the test clock.

## Forecloses

- `chorusd` must share the satellites' link. Under docker-compose's bridge
  network a multicast query never reaches the LAN, so a household there
  either runs `chorusd` with host networking or pins every address.
- IPv4 only, as the satellites' link is; an AAAA answer is ignored.
- One network interface: the query leaves by the host's default multicast
  route. A host with a second LAN on another interface pins the satellites
  on it.
