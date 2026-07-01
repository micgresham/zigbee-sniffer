// transport_usb.h — framed binary I/O over USB Serial/JTAG.
#pragma once

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

void transport_usb_init(void);

// Write raw bytes (a fully framed message) to the host. Blocks briefly.
int transport_usb_write(const uint8_t *data, size_t len);

// Read up to `len` bytes, waiting up to timeout_ms. Returns bytes read (>=0).
int transport_usb_read(uint8_t *data, size_t len, uint32_t timeout_ms);

#ifdef __cplusplus
}
#endif
