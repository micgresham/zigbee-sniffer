// Package names — OUI (manufacturer) identification for 802.15.4 extended
// (IEEE) addresses. The top 3 bytes of an extended address are the IEEE OUI,
// which identifies the silicon/vendor. This lets us label foreign networks and
// devices by manufacturer with no integration ("that PAN is Philips Hue").
//
// This is a curated starter set of vendors common on Zigbee/Thread networks;
// add rows freely — the key is the 24-bit OUI (top 3 bytes of the MAC).
package names

// ouiVendors maps a 24-bit OUI (top 3 bytes of an extended address) to a vendor
// label. Values chosen to read well in the UI ("Philips Hue" rather than
// "Signify"). Extend as needed.
var ouiVendors = map[uint32]string{
	0x001788: "Philips Hue", // Signify (Philips Hue bridge + bulbs)
	0x00158D: "Xiaomi/Aqara", // Lumi United (Jennic/NXP silicon)
	0x54EF44: "Aqara",         // Lumi United
	0x04CF8C: "Xiaomi",        // Xiaomi
	0x8CF681: "Xiaomi/Aqara",
	0x000B57: "Silicon Labs", // Ember/SiLabs (IKEA, SmartThings, many)
	0x000D6F: "Silicon Labs", // Ember
	0x0CAE7B: "Espressif",    // ESP (Thread/Zigbee)
	0x00124B: "Texas Instruments",
	0x00212E: "Texas Instruments",
	0x14B457: "SmartThings", // Samsung
	0x286D97: "SmartThings",
	0x680AE2: "Sonos",
	0xB0CE18: "Sengled",
	0x94DEB8: "Sonoff/ITEAD",
	0xDC8E95: "Tuya",
	0xA4C138: "Tuya",         // Tuya/MOES (common)
	0x84FD27: "Bosch",
}

// vendorForExt returns the manufacturer label for an extended address, or "".
func vendorForExt(ext uint64) string {
	oui := uint32((ext >> 40) & 0xFFFFFF)
	return ouiVendors[oui]
}
