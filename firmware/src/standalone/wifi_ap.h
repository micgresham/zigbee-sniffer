// wifi_ap.h — bring up WiFi for the standalone web UI (SoftAP, or STA join).
#pragma once

#include "config_nvs.h"

#ifdef __cplusplus
extern "C" {
#endif

// Start WiFi per config: SoftAP (default, SSID/pass from cfg) or STA join.
// Returns the IP address string the UI is reachable at (static buffer).
const char *wifi_start(const device_config_t *cfg);

// Number of currently-associated SoftAP clients. Used to gate 802.15.4 capture:
// the radio is shared, so we keep it off WiFi's back until a client has joined.
int wifi_sta_count(void);

#ifdef __cplusplus
}
#endif
