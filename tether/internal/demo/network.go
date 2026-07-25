// Package demo simulates a live capture — a medium-sized home Zigbee mesh, a
// Philips Hue foreign network, an unrelated foreign Zigbee network on a
// different channel, and a Thread/Matter network on a third channel — so the
// dashboard can be exercised/demoed with no hardware attached. It feeds the
// exact same ingest() pipeline real hardware
// does (see source.go), by constructing real, correctly-encrypted MPDUs
// through the same decode/crypto code paths used to verify captures, rather
// than special-casing the UI for simulated data.
package demo

import "zbsniff/internal/names"

// NetworkKey is the fixed demo network key. DecodeFrame decrypts every home
// frame through it exactly as it would a real network key from a ZHA backup.
var NetworkKey = []byte{
	0xd0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77,
	0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xf0,
}

const (
	HomePAN    = 0xd3a1
	HomeChan   = 20
	HuePAN     = 0xbaa6
	HueChan    = 25
	ForeignPAN = 0x9e42
	ForeignChan = 15
	ThreadPAN  = 0x7c14
	ThreadChan = 26

	Coordinator uint16 = 0x0000
)

// Device is one simulated node on the home mesh.
type Device struct {
	Short  uint16
	Ext    uint64
	Name   string
	Area   string
	Type   string // "Router" or "EndDevice" (device_type, matches HA's own vocabulary)
	Parent uint16 // immediate MAC-layer neighbor it routes through (Coordinator for top-level routers)
	Mfg    string
}

// oui packs a 24-bit OUI + a device-specific low 40 bits into an extended addr.
func oui(o uint32, low uint64) uint64 { return (uint64(o) << 40) | (low & 0xFFFFFFFFFF) }

const (
	ouiSiliconLabs = 0x000B57
	ouiSonoff      = 0x94DEB8
	ouiTI          = 0x00124B
	ouiHue         = 0x001788
	ouiSengled     = 0xB0CE18
	ouiNordic      = 0xF4CE36
)

// HomeDevices is the simulated mesh: coordinator implied, plus routers and end
// devices with a plausible 1-2 hop topology (a couple of routers reach the
// coordinator only through another router, so the routing tree has real depth).
var HomeDevices = []Device{
	{Short: 0x1001, Ext: oui(ouiSonoff, 0x0001), Name: "Living Room Plug", Area: "Living Room", Type: "Router", Parent: Coordinator, Mfg: "Sonoff/ITEAD"},
	{Short: 0x1002, Ext: oui(ouiSonoff, 0x0002), Name: "Kitchen Switch", Area: "Kitchen", Type: "Router", Parent: Coordinator, Mfg: "Sonoff/ITEAD"},
	{Short: 0x1003, Ext: oui(ouiSiliconLabs, 0x0003), Name: "Hallway Repeater", Area: "Hallway", Type: "Router", Parent: Coordinator, Mfg: "Silicon Labs"},
	{Short: 0x1004, Ext: oui(ouiSiliconLabs, 0x0004), Name: "Garage Router", Area: "Garage", Type: "Router", Parent: 0x1003, Mfg: "Silicon Labs"},

	{Short: 0x2001, Ext: oui(ouiSiliconLabs, 0x1001), Name: "Living Room Motion", Area: "Living Room", Type: "EndDevice", Parent: 0x1001, Mfg: "Silicon Labs"},
	{Short: 0x2002, Ext: oui(ouiSiliconLabs, 0x1002), Name: "Kitchen Temp/Humidity", Area: "Kitchen", Type: "EndDevice", Parent: 0x1002, Mfg: "Silicon Labs"},
	{Short: 0x2003, Ext: oui(ouiSiliconLabs, 0x1003), Name: "Bedroom Door Sensor", Area: "Bedroom", Type: "EndDevice", Parent: Coordinator, Mfg: "Silicon Labs"},
	{Short: 0x2004, Ext: oui(ouiSiliconLabs, 0x1004), Name: "Office Desk Light", Area: "Office", Type: "EndDevice", Parent: 0x1002, Mfg: "Silicon Labs"},
	{Short: 0x2005, Ext: oui(ouiSiliconLabs, 0x1005), Name: "Bathroom Leak Sensor", Area: "Bathroom", Type: "EndDevice", Parent: 0x1003, Mfg: "Silicon Labs"},
	{Short: 0x2006, Ext: oui(ouiSiliconLabs, 0x1006), Name: "Garage Door Sensor", Area: "Garage", Type: "EndDevice", Parent: 0x1004, Mfg: "Silicon Labs"},
	{Short: 0x2007, Ext: oui(ouiSiliconLabs, 0x1007), Name: "Front Door Lock", Area: "Entryway", Type: "EndDevice", Parent: Coordinator, Mfg: "Silicon Labs"},
	{Short: 0x2008, Ext: oui(ouiSiliconLabs, 0x1008), Name: "Kids Room Sensor", Area: "Kids Room", Type: "EndDevice", Parent: 0x1001, Mfg: "Silicon Labs"},
}

// HueDevices sit on the separate Hue bridge network (foreign to the home PAN),
// same channel as the home network here — mirroring how a real Hue bridge
// commonly shares the channel a nearby Zigbee coordinator picked.
var HueDevices = []Device{
	{Short: 0x3001, Ext: oui(ouiHue, 0x0001), Name: "Hue Bridge", Area: "", Type: "Coordinator", Parent: 0, Mfg: "Philips Hue"},
	{Short: 0x3002, Ext: oui(ouiHue, 0x0002), Name: "Hue Bulb Kitchen", Area: "Kitchen", Type: "EndDevice", Parent: 0x3001, Mfg: "Philips Hue"},
	{Short: 0x3003, Ext: oui(ouiHue, 0x0003), Name: "Hue Bulb Living Room", Area: "Living Room", Type: "EndDevice", Parent: 0x3001, Mfg: "Philips Hue"},
	{Short: 0x3004, Ext: oui(ouiHue, 0x0004), Name: "Hue Bulb Bedroom", Area: "Bedroom", Type: "EndDevice", Parent: 0x3001, Mfg: "Philips Hue"},
}

// ForeignDevices are a neighbor's unrelated Zigbee network on a different
// channel entirely — never resolved to a name (no integration would ever
// report them), just an OUI-labeled foreign PAN passively overheard.
var ForeignDevices = []Device{
	{Short: 0x4001, Ext: oui(ouiSengled, 0x0001), Name: "", Area: "", Type: "Router", Parent: 0, Mfg: "Sengled"},
	{Short: 0x4002, Ext: oui(ouiSengled, 0x0002), Name: "", Area: "", Type: "EndDevice", Parent: 0x4001, Mfg: "Sengled"},
}

// ThreadDevices are a neighbor's Thread/Matter mesh — a third foreign network,
// on its own channel, whose traffic uses 6LoWPAN framing (not Zigbee NWK) so
// it exercises the decoder's Thread/Matter detection (see
// decode.isSixLowPANDispatch) rather than the OUI-labeling path the other two
// foreign networks exercise. Never resolved to a name, same as ForeignDevices
// — the network itself is identified by protocol shape, not by device.
var ThreadDevices = []Device{
	{Short: 0x5001, Ext: oui(ouiNordic, 0x0001), Name: "", Area: "", Type: "Router", Parent: 0, Mfg: "Nordic Semiconductor"},
	{Short: 0x5002, Ext: oui(ouiNordic, 0x0002), Name: "", Area: "", Type: "EndDevice", Parent: 0x5001, Mfg: "Nordic Semiconductor"},
}

// path returns the MAC-layer hop chain from a device up to the coordinator,
// e.g. [device, parent, grandparent, ..., Coordinator] — each consecutive
// pair is one physical hop the sniffer would witness as its own MAC frame.
func path(d Device) []uint16 {
	byShort := map[uint16]Device{}
	for _, hd := range HomeDevices {
		byShort[hd.Short] = hd
	}
	out := []uint16{d.Short}
	cur := d
	for cur.Parent != Coordinator {
		out = append(out, cur.Parent)
		next, ok := byShort[cur.Parent]
		if !ok {
			break
		}
		cur = next
	}
	out = append(out, Coordinator)
	return out
}

// PopulateRegistry seeds the name/area/device-type/manufacturer registry as if
// Home Assistant (ZHA) and a paired Hue bridge had already reported everything
// — exactly the steady-state a real long-running capture reaches once both
// integrations finish their first sync.
func PopulateRegistry(reg *names.Registry) {
	reg.Set(Coordinator, "Coordinator")
	reg.SetExt(coordinatorExt, "Coordinator")
	reg.SetShortExt(Coordinator, coordinatorExt)
	reg.SetInfo(Coordinator, names.DeviceInfo{
		Manufacturer: "Texas Instruments", Model: "CC2652P", DeviceType: "Coordinator",
		Available: true, HasAvailable: true,
	})
	for _, d := range HomeDevices {
		reg.Set(d.Short, d.Name)
		reg.SetExt(d.Ext, d.Name)
		reg.SetShortExt(d.Short, d.Ext)
		reg.SetArea(d.Ext, d.Area)
		reg.SetInfo(d.Short, names.DeviceInfo{
			Manufacturer: d.Mfg, Model: modelFor(d), DeviceType: d.Type,
			Available: true, HasAvailable: true,
		})
		reg.SetNet(d.Short, intPtr(hashLQI(d.Short)), nil)
	}
	for _, d := range HueDevices {
		reg.SetExt(d.Ext, d.Name)
		reg.SetShortExt(d.Short, d.Ext)
	}
	// ForeignDevices deliberately not named/labeled — a real neighbor network
	// never resolves through the user's own HA/Hue integrations.
}

const coordinatorExt = 0x00124b0001000001

func intPtr(v int) *int { return &v }

// hashLQI gives each device a stable (not random-per-refresh), plausible LQI
// so the UI's link-quality coloring stays consistent across restarts.
func hashLQI(short uint16) int {
	v := int(short%40) + 190 // ~190-229, "good" range
	if v > 255 {
		v = 255
	}
	return v
}

func modelFor(d Device) string {
	switch d.Type {
	case "Router":
		return "Smart Plug"
	default:
		return "Sensor"
	}
}
