// beacon.h — active network discovery: transmit an 802.15.4 MAC *beacon request*
// on the current channel. Any coordinator or router in range replies with a
// beacon whose MAC header carries that network's PAN id — so we discover even
// idle neighbouring networks that aren't otherwise transmitting. The beacon
// replies arrive through the normal promiscuous RX path and are captured/decoded
// like any other frame (the host records their source PAN).
//
// This is the active counterpart to passively listening for traffic: it solicits
// a response rather than waiting for one. It TRANSMITS a single broadcast MAC
// command frame (no addressing to any specific device, no network join, no key),
// which is exactly what a normal Zigbee scan does.
#pragma once

#ifdef __cplusplus
extern "C" {
#endif

// Transmit one beacon request on the current channel. Replies (beacons) are
// received via the ordinary capture path.
void beacon_request_send(void);

#ifdef __cplusplus
}
#endif
