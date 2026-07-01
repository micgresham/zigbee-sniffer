package serialio

import (
	"fmt"
	"strings"

	"go.bug.st/serial/enumerator"
)

// espressifVID is the USB vendor ID for the ESP32-C6 native USB-Serial/JTAG,
// which is the port the `tethered` firmware streams the binary protocol on
// (the UART-bridge port has a different VID and carries console text).
const espressifVID = "303A"

// AutoDetect returns serial ports that look like an ESP32 native-USB device.
func AutoDetect() ([]string, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range ports {
		if p.IsUSB && strings.EqualFold(p.VID, espressifVID) {
			out = append(out, p.Name)
		}
	}
	return out, nil
}

// DescribePorts returns a human-readable list of all serial ports (for errors).
func DescribePorts() string {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil || len(ports) == 0 {
		return "  (none found)"
	}
	var b strings.Builder
	for _, p := range ports {
		desc := p.Name
		if p.IsUSB {
			desc += fmt.Sprintf("  [USB %s:%s %s]", p.VID, p.PID, p.Product)
		}
		b.WriteString("  " + desc + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
