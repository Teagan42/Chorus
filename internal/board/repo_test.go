package board

import (
	"slices"
	"testing"
)

// The checked-in files, not fixtures: the main board the design doc
// describes, the Satellite1 kit it sits under, and the Satellite1 config
// that already runs Chorus in the living room.
const (
	repoBoard      = "../../hardware/chorus-main"
	repoPins       = repoBoard + "/pins.yaml"
	repoKit        = repoBoard + "/satellite1-hat.yaml"
	repoSatellite1 = "../../esphome/satellite1.yaml"
)

func TestTheMainBoardPinMapChecksClean(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Check(); err != nil {
		t.Fatalf("hardware/chorus-main/pins.yaml: %v", err)
	}
}

// verifies SPEC §3.3.2
// The kit file is FutureProofHomes' wiring as the config that runs today
// drives it: every GPIO the config sets that reaches the expansion connector
// is on the pad the kit file says, and marked as the kit's own, so no main
// board can take it.
func TestTheKitPinoutIsTheWiringSatellite1YAMLDrives(t *testing.T) {
	k, err := LoadKit(repoKit)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Check(); err != nil {
		t.Fatal(err)
	}
	if err := k.Mirrors(repoSatellite1); err != nil {
		t.Fatal(err)
	}
	used, err := ConfigGPIOs(repoSatellite1)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range k.Pads {
		if p.GPIO == nil {
			continue
		}
		paths, driven := used[*p.GPIO]
		switch {
		case driven && p.free():
			t.Errorf("pad %s (GPIO%d) is free in the kit file, but satellite1.yaml sets it at %v", p.Pad, *p.GPIO, paths)
		case driven && !slices.Contains(paths, p.Satellite1):
			t.Errorf("pad %s (GPIO%d) does not say where satellite1.yaml sets it (%v)", p.Pad, *p.GPIO, paths)
		}
	}
	// The XMOS bus, the PD controller's I2C and the radar's UART, which is
	// what makes the kit's audio work at all, are all annotated.
	for _, net := range []string{
		"I2S_LRCLK", "I2S_BCLK", "I2S_MCLK", "I2S_DOUT", "I2S_DIN",
		"XMOS_RST", "XMOS_SPI_CS_N", "XMOS_SPI_SCLK", "XMOS_SPI_MOSI", "XMOS_SPI_MISO",
		"I2C_SDA", "I2C_SCL", "I2C_IRQ", "RADAR_TX", "RADAR_RX",
	} {
		found := false
		for _, p := range k.Pads {
			if p.Net == net {
				found = true
				if p.Satellite1 == "" {
					t.Errorf("%s on pad %s does not say where satellite1.yaml drives it", net, p.Pad)
				}
			}
		}
		if !found {
			t.Errorf("%s is not on the kit's connector", net)
		}
	}
}

// The main board's pins are ones the firmware that runs today leaves alone,
// so flashing chorus_bridge with Ethernet added changes nothing the XMOS,
// the amplifier or the radar depends on.
func TestTheMainBoardTakesNoPinTheFirmwareDrives(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	used, err := ConfigGPIOs(repoSatellite1)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range b.Pins {
		if paths, ok := used[p.GPIO]; ok {
			t.Errorf("%s is on GPIO%d, which satellite1.yaml already sets at %v", p.Net, p.GPIO, paths)
		}
	}
}

// verifies SPEC §3.3.2
// The uplink and the downlink share one radio, and that is the one thing
// the main board is for: wired Ethernet. The W5500's reset, interrupt and
// select are where FutureProofHomes' own shoe puts them, so their board and
// ours are wired alike, and its SPI is its own, off the XMOS bus.
func TestTheW5500IsWhereTheShoeHasItOnItsOwnBus(t *testing.T) {
	b, err := Load(repoPins)
	if err != nil {
		t.Fatal(err)
	}
	for net, pad := range map[string]string{
		"ETH_RST_N": "13", "ETH_INT_N": "33", "ETH_CS_N": "37", "POE_SENSE": "36",
	} {
		if p := b.Net(net); p == nil || p.Pad != pad {
			t.Errorf("%s = %+v, want pad %s, as the shoe", net, p, pad)
		}
	}
	k, err := LoadKit(repoKit)
	if err != nil {
		t.Fatal(err)
	}
	for _, net := range []string{"ETH_SCLK", "ETH_MOSI", "ETH_MISO"} {
		p := b.Net(net)
		if p == nil {
			t.Errorf("%s is not on the board", net)
			continue
		}
		if kp, _ := k.Pad(p.Pad); !kp.free() {
			t.Errorf("%s is on pad %s, which the kit uses: %s", net, p.Pad, kp.Use)
		}
	}
}

// verifies SPEC §3.3.2
// The main board checks clean and holds every pad of its receptacle to the
// HAT's plug, so the netlist Pcbnew imports is the board that mates with
// the kit on the bench.
func TestTheMainSchematicChecksClean(t *testing.T) {
	s, err := LoadSchematic(repoBoard)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check(); err != nil {
		t.Fatalf("hardware/chorus-main:\n%v", err)
	}
}

// PoE feeds the HAT's VBUS through a diode and nothing else: the HAT's own
// USB-C diode makes the pair an OR, and its 5 V rail is never driven from
// below.
func TestPoEReachesTheHATOnlyThroughVBUS(t *testing.T) {
	s, err := LoadSchematic(repoBoard)
	if err != nil {
		t.Fatal(err)
	}
	nets, errs := s.Nets()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	refs := s.refs(new([]error))
	partOf := func(n Node) string { return refs[n.Ref].Part }
	var diode string
	for _, n := range nets["VBUS"] {
		if partOf(n) == "SS32" && n.Name == "K" {
			diode = n.Ref
		}
	}
	if diode == "" {
		t.Fatalf("VBUS = %s, want a Schottky's cathode on it", nodeList(nets["VBUS"]))
	}
	found := false
	for _, n := range nets["POE_12V"] {
		found = found || n.Ref == diode && n.Name == "A"
	}
	if !found {
		t.Errorf("%s's anode is not on POE_12V: %s", diode, nodeList(nets["POE_12V"]))
	}
	for _, n := range nets["5V0"] {
		if n.Type == "power_out" {
			t.Errorf("%s drives the HAT's 5 V rail", n.Ref+"."+n.Name)
		}
	}
}

// Every footprint of the board's own is in chorus-main.pretty, so a KiCad 9
// import resolves all of them with nothing to download first, and the check
// above has held each to its part's pads.
func TestEveryMainBoardFootprintIsDrawn(t *testing.T) {
	s, err := LoadSchematic(repoBoard)
	if err != nil {
		t.Fatal(err)
	}
	for _, fp := range s.Undrawn() {
		t.Errorf("parts.yaml names %s, which chorus-main.pretty lacks", fp)
	}
}
