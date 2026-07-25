// ota.h — self firmware update over the serial link, using esp_ota. The host
// streams the .bin in chunks (CMD_OTA_BEGIN/DATA/END); we write the inactive OTA
// slot, verify, mark it bootable, and reboot. Requires an OTA partition table.
//
// SAFETY: the bootloader rolls back to the old slot if the new image fails to
// confirm, and USB reflashing is always available as a fallback.
#pragma once

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

esp_err_t ota_begin(uint32_t total, uint32_t crc32);
esp_err_t ota_write(const uint8_t *data, uint16_t len);
esp_err_t ota_end(void);   // on success the caller should reboot shortly after
void      ota_abort(void);

uint8_t  ota_state(void);     // OTA_* (see proto.h)
uint32_t ota_received(void);
uint32_t ota_total(void);
uint8_t  ota_err(void);

#ifdef __cplusplus
}
#endif
