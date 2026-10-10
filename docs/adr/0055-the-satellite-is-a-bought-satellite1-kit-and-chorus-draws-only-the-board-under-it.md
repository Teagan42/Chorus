# 0055. The satellite is a bought Satellite1 kit, and Chorus draws only the board under it

- **Status:** proposed
- **Source:** SPEC §3.3, §3.3.2, §3.2.1, §5 · ADR-0047, ADR-0053 ·
  FutureProofHomes/Satellite1-Hardware `2eb08ff`, Satellite1-ESPHome `46511ed`

Supersedes [ADR-0047](0047-the-satellite-board-keeps-the-reference-voice-frontend-and-adds-a-wire.md).
The household's satellites are FutureProofHomes Satellite1 kits, HAT and Core,
bought and unmodified. The HAT and the Core are theirs, so which part sits on
which of them is theirs too. Chorus designs one board, the
[main board](../hardware/README.md) (`hardware/chorus-main`). It mates with the
HAT's 80-pin expansion connector, J7, where FutureProofHomes' own prototype
"mini speaker shoe" mates. It adds what SPEC §3.3.2 measured as the limit no
firmware recovers: wired Ethernet (a W5500), with 802.3af PoE feeding the HAT's
VBUS. It also gives the speaker a connector in the base.

The kit's J7 pinout is recorded as data, `satellite1-hat.yaml`, from the HAT's
schematic and the Core's header. `internal/board` holds the main board's
receptacle to it pad by pad on every `task test`:

- a pad the HAT leaves unconnected stays unconnected;
- every kit ground is grounded;
- a pad the kit uses carries only the kit's own net;
- the main board takes only GPIOs that nothing on the kit drives, and that
  `esphome/satellite1.yaml` never sets.

The W5500's reset, interrupt and select sit on the shoe's pads (GPIO2, 38 and
3). Its clock and data take three more free pads (GPIO39, 41 and 14), so
Ethernet never waits on the XMOS's SPI bus as it does on the shoe.

The acoustic argument of ADR-0047 survives unchanged and gets stronger. Every
Chorus satellite is now a Satellite1, so voiceprints enrolled at one match at the
next (§5) with no frontend redrawn from a PDF. What ADR-0047 added to the
frontend goes, because it was never Chorus's board to add to:

- **Eight mics.** The HAT has the Satellite1's four.
- **The amplifier's sensed current** (`AMP_SENSE`). The HAT leaves the
  TAS2780's SDOUT unconnected and J7 does not carry it, so the truncation
  point's fixed delay (§3.2.1) is measured on a bench, not per board.
- **The hardware mute switch.** It is the HAT's.

[ADR-0053](0053-direction-of-arrival-is-estimated-on-the-host-from-the-raw-array-and-the-reference.md)
still holds with four mics instead of eight. Its link budget shrinks, and its
spatial aliasing begins at about 3.8 kHz instead of 7 kHz. That is the
alternative ADR-0047 rejected, now the only one the kit allows.

The receptacle's footprint is drawn here. Its signal pads come from
FutureProofHomes' shoe and protoboard: the pin-1 mark, which row is routed,
and the protoboard's "1" and "41". Its four power contacts are inferred, and
`parts.yaml` marks the part `unverified`. Each `task gen:hardware` says so
until a vendor footprint, or a meter on a powered HAT, settles which contact is
VBUS and which is 5 V. The status stays proposed until a main board under a kit
passes `task test:hardware` over its cable.

## Alternatives rejected

- **Our own HAT and Core, following the Satellite1's split.** Rejected by its
  owner: the household already has the kits, and FutureProofHomes decides
  what each of their boards carries. Redrawing their frontend buys nothing
  that the kit does not already ship.
- **The shoe as is.** It is published as a PDF and a 3D model, with no
  source to edit or order from. Its W5500 also shares the XMOS's SPI bus,
  and that bus is what must answer before any audio flows (§3.3.2).

## Forecloses

Anything the kit does not route to J7. That covers a raw-mic tap, the
amplifier's sense output, the mute switch's state and more mics. Each of
these is now a firmware change on the XU316 or a different kit, not a main
board revision. A new HAT revision that moves a J7 pad is a new kit file, and
the checks then show what moved.
