package decode

// Human-readable names for Zigbee identifiers, for richer frame summaries and
// diagnostics. Not exhaustive — covers the common Home Automation set.

// Clusters maps ZCL cluster IDs to names.
var Clusters = map[int]string{
	0x0000: "Basic", 0x0001: "Power Config", 0x0003: "Identify", 0x0004: "Groups",
	0x0005: "Scenes", 0x0006: "On/Off", 0x0007: "On/Off Switch Cfg", 0x0008: "Level Control",
	0x000A: "Time", 0x000F: "Binary Input", 0x0019: "OTA Upgrade", 0x0020: "Poll Control",
	0x0102: "Window Covering", 0x0201: "Thermostat", 0x0202: "Fan Control",
	0x0204: "Thermostat UI", 0x0300: "Color Control", 0x0400: "Illuminance",
	0x0402: "Temperature", 0x0403: "Pressure", 0x0404: "Flow", 0x0405: "Humidity",
	0x0406: "Occupancy", 0x0500: "IAS Zone", 0x0501: "IAS ACE", 0x0502: "IAS WD",
	0x0702: "Metering", 0x0B04: "Electrical Meas", 0x0B05: "Diagnostics", 0xEF00: "Tuya",
	0x0021: "Green Power", 0xFC01: "Manufacturer", 0xFCC0: "Xiaomi",
}

// ClusterName returns the cluster name or a hex fallback.
func ClusterName(id int) string {
	if n, ok := Clusters[id]; ok {
		return n
	}
	return ""
}

// NWK command frame IDs.
var nwkCommands = map[byte]string{
	0x01: "Route Request", 0x02: "Route Reply", 0x03: "Network Status",
	0x04: "Leave", 0x05: "Route Record", 0x06: "Rejoin Request",
	0x07: "Rejoin Response", 0x08: "Link Status", 0x09: "Network Report",
	0x0A: "Network Update", 0x0B: "End Device Timeout Req", 0x0C: "End Device Timeout Resp",
}

// NWK "Network Status" command status codes — these are the route/link failures
// that often precede a device going unresponsive (gold for diagnostics).
var nwkStatusCodes = map[byte]string{
	0x00: "No route available", 0x01: "Tree link failure", 0x02: "Non-tree link failure",
	0x03: "Low battery", 0x04: "No routing capacity", 0x05: "No indirect capacity",
	0x06: "Indirect transaction expiry", 0x07: "Target device unavailable",
	0x08: "Target address unallocated", 0x09: "Parent link failure",
	0x0A: "Validate route", 0x0B: "Source route failure", 0x0C: "Many-to-one route failure",
	0x0D: "Address conflict", 0x0E: "Verify addresses", 0x0F: "PAN identifier update",
	0x10: "Network address update", 0x11: "Bad frame counter", 0x12: "Bad key sequence number",
}

// ZCL global command names.
var zclGlobalNames = map[byte]string{
	0x00: "Read Attributes", 0x01: "Read Attributes Rsp", 0x02: "Write Attributes",
	0x03: "Write Attributes Undivided", 0x04: "Write Attributes Rsp", 0x05: "Write Attributes No Rsp",
	0x06: "Configure Reporting", 0x07: "Configure Reporting Rsp", 0x08: "Read Reporting Cfg",
	0x09: "Read Reporting Cfg Rsp", 0x0A: "Report Attributes", 0x0B: "Default Response",
	0x0C: "Discover Attributes", 0x0D: "Discover Attributes Rsp",
}

// A few cluster-specific command names (cluster<<8 | cmd, client→server).
var zclClusterCmds = map[int]string{
	0x0006<<8 | 0x00: "Off", 0x0006<<8 | 0x01: "On", 0x0006<<8 | 0x02: "Toggle",
	0x0008<<8 | 0x00: "Move to Level", 0x0008<<8 | 0x01: "Move", 0x0008<<8 | 0x02: "Step",
	0x0008<<8 | 0x04: "Move to Level (OnOff)",
	0x0300<<8 | 0x0A: "Move to Color Temp",
	0x0500<<8 | 0x00: "Zone Status Change", 0x0501<<8 | 0x00: "Arm",
}
