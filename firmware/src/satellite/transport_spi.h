// transport_spi.h — SPI-slave transport: a satellite forwards framed captures to
// the primary over a shared SPI bus, asserting DATA_READY when it has data.
// See docs/multi-radio.md and docs/carrier.md.
#pragma once

#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

// Pin assignments — verified against the ESP32-C6 SuperMini's actual broken-out
// GPIOs (0-9, 12-23; GPIO10/11 are NOT brought out to a pad on this board and
// must not be used). See docs/carrier.md.
#define SAT_PIN_SCK        6
#define SAT_PIN_MOSI       7
#define SAT_PIN_MISO       2
#define SAT_PIN_CS         4
#define SAT_PIN_DATA_READY 3
#define SAT_PIN_SYNC       5
#define SAT_SPI_XFER       256   // fixed transaction size, bytes

// Command bytes from the primary are de-framed and delivered here.
typedef void (*spi_command_cb_t)(uint8_t type, const uint8_t *payload, uint16_t len);

void spi_slave_transport_init(spi_command_cb_t cb);

// Queue a fully-framed message for transmission to the primary (non-blocking).
// Returns false if the TX ring is full (caller may count a drop).
bool spi_slave_send(const uint8_t *data, size_t len);

#ifdef __cplusplus
}
#endif
