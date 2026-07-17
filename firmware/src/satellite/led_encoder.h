// led_encoder.h — RMT byte encoder for a WS2812 pixel (bytes + reset code).
// Ported from the ESP-IDF `peripherals/rmt/led_strip` example; see led.c for
// the satellite-specific single-pixel usage.
#pragma once

#include <stdint.h>
#include "driver/rmt_encoder.h"

#ifdef __cplusplus
extern "C" {
#endif

typedef struct {
    uint32_t resolution; // encoder resolution, in Hz
} sat_led_encoder_config_t;

esp_err_t sat_led_new_encoder(const sat_led_encoder_config_t *config, rmt_encoder_handle_t *ret_encoder);

#ifdef __cplusplus
}
#endif
