// Command probe validates the Noise transport against a real satellite and
// dumps what the device exposes. First step of the phase-1 spike: prove we can
// own the connection without Home Assistant (docs/SPEC.md §14).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	"github.com/teaganglenn/chorus/internal/config"
	"github.com/teaganglenn/chorus/internal/esphome"
	"github.com/teaganglenn/chorus/internal/msgid"
	"github.com/teaganglenn/chorus/internal/pb"
)

func main() {
	inventory := flag.String("inventory", "devices.yaml", "satellite inventory path")
	device := flag.String("device", "", "satellite name (default: the only one)")
	listen := flag.Duration("listen", 0, "after probing, print incoming messages for this long")
	flag.Parse()

	if err := run(*inventory, *device, *listen); err != nil {
		log.Fatalf("probe failed: %v", err)
	}
}

func run(inventory, device string, listen time.Duration) error {
	cfg, err := config.Load(inventory)
	if err != nil {
		return err
	}
	sat, err := cfg.Find(device)
	if err != nil {
		return err
	}

	fmt.Printf("message ids known from descriptors: %d\n", msgid.Known())
	fmt.Printf("dialing %s (%s)...\n", sat.Name, sat.Address)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, err := esphome.Dial(ctx, sat.Address, sat.PSK)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	fmt.Printf("\n=== handshake OK ===\nnode: %s\napi:  %d.%d\n\n", c.Name(), c.APIVersion[0], c.APIVersion[1])
	fmt.Println("=== device info ===")
	fmt.Println(prototext.Format(c.DeviceInfoMsg))

	if err := listEntities(c); err != nil {
		return err
	}
	if listen > 0 {
		return tail(c, listen)
	}
	return nil
}

// listEntities enumerates components so we can confirm what the stock firmware
// exposes: mic, speaker, micro_wake_word, LED ring.
func listEntities(c *esphome.Client) error {
	if err := c.Send(&pb.ListEntitiesRequest{}); err != nil {
		return err
	}
	fmt.Println("=== entities ===")
	for {
		m, err := c.Recv()
		if err != nil {
			if esphome.IsUnknownMessage(err) {
				continue
			}
			return err
		}
		if _, done := m.(*pb.ListEntitiesDoneResponse); done {
			fmt.Println()
			return nil
		}
		summarize(m)
	}
}

func summarize(m proto.Message) {
	name := string(m.ProtoReflect().Descriptor().Name())
	switch e := m.(type) {
	case *pb.ListEntitiesSelectResponse:
		fmt.Printf("  select       %-34s options=%v\n", e.GetName(), e.GetOptions())
	case *pb.ListEntitiesSwitchResponse:
		fmt.Printf("  switch       %-34s\n", e.GetName())
	case *pb.ListEntitiesLightResponse:
		fmt.Printf("  light        %-34s effects=%v\n", e.GetName(), e.GetEffects())
	case *pb.ListEntitiesMediaPlayerResponse:
		fmt.Printf("  media_player %-34s\n", e.GetName())
	case *pb.ListEntitiesNumberResponse:
		fmt.Printf("  number       %-34s %.1f..%.1f\n", e.GetName(), e.GetMinValue(), e.GetMaxValue())
	case *pb.ListEntitiesSensorResponse:
		fmt.Printf("  sensor       %-34s %s\n", e.GetName(), e.GetUnitOfMeasurement())
	case *pb.ListEntitiesTextSensorResponse:
		fmt.Printf("  text_sensor  %-34s\n", e.GetName())
	case *pb.ListEntitiesBinarySensorResponse:
		fmt.Printf("  binary       %-34s %s\n", e.GetName(), e.GetDeviceClass())
	case *pb.ListEntitiesButtonResponse:
		fmt.Printf("  button       %-34s\n", e.GetName())
	default:
		fmt.Printf("  %s\n", name)
	}
}

// tail prints unsolicited traffic. Useful for watching what the device emits on
// wake word with voice_assistant still installed.
func tail(c *esphome.Client, d time.Duration) error {
	fmt.Printf("=== listening %s (ctrl-c to stop) ===\n", d)
	deadline := time.Now().Add(d)
	go func() {
		time.Sleep(d)
		_ = c.Close() // Unblocks the Recv loop below.
	}()
	for time.Now().Before(deadline) {
		m, err := c.Recv()
		if err != nil {
			if esphome.IsUnknownMessage(err) {
				continue
			}
			if time.Now().After(deadline.Add(-time.Second)) {
				return nil
			}
			fmt.Fprintf(os.Stderr, "recv: %v\n", err)
			return nil
		}
		fmt.Printf("  <- %s %s\n",
			m.ProtoReflect().Descriptor().Name(),
			oneLine(prototext.MarshalOptions{Multiline: false}.Format(m)))
	}
	return nil
}

func oneLine(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
