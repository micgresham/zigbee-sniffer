// config_nvs.h — persisted device settings (NVS).
#pragma once

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint8_t  channel;        // 11..26
    uint8_t  mode;           // zb_mode_t
    uint8_t  radio_id;       // 0=primary, 1..3 satellites
    uint32_t hop_mask;       // channels in the hop/ED set
    uint16_t hop_dwell_ms;   // 0 = fixed channel
    bool     key_present;
    uint8_t  nwk_key[16];    // Zigbee network key (optional, for on-device decode)
    // WiFi (standalone build)
    char     wifi_ssid[33];
    char     wifi_pass[65];
    bool     wifi_sta;       // true = join SSID, false = SoftAP
    bool     satellites_enabled;  // primary: poll SPI satellites (carrier multi-radio)
} device_config_t;

void config_load(device_config_t *cfg);   // loads, applying defaults if unset
void config_save(const device_config_t *cfg);
void config_defaults(device_config_t *cfg);

#ifdef __cplusplus
}
#endif
