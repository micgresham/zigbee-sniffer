// led_encoder.c — see led_encoder.h.
// Ported near-verbatim from the ESP-IDF `peripherals/rmt/led_strip` example
// (the standard way to drive a WS2812 over RMT): a bytes-encoder using
// WS2812's T0H/T0L/T1H/T1L timings, followed by a copy-encoder emitting the
// >=50us reset/latch code.
#if defined(BUILD_SATELLITE)

#include <stdlib.h>
#include "esp_check.h"
#include "led_encoder.h"

static const char *TAG = "sat-led-enc";

typedef struct {
    rmt_encoder_t base;
    rmt_encoder_t *bytes_encoder;
    rmt_encoder_t *copy_encoder;
    int state;
    rmt_symbol_word_t reset_code;
} sat_led_rmt_encoder_t;

static size_t sat_led_encode(rmt_encoder_t *encoder, rmt_channel_handle_t channel,
                              const void *primary_data, size_t data_size, rmt_encode_state_t *ret_state)
{
    sat_led_rmt_encoder_t *enc = __containerof(encoder, sat_led_rmt_encoder_t, base);
    rmt_encoder_handle_t bytes_encoder = enc->bytes_encoder;
    rmt_encoder_handle_t copy_encoder = enc->copy_encoder;
    rmt_encode_state_t session_state = RMT_ENCODING_RESET;
    rmt_encode_state_t state = RMT_ENCODING_RESET;
    size_t encoded_symbols = 0;
    switch (enc->state) {
    case 0: // send RGB data
        encoded_symbols += bytes_encoder->encode(bytes_encoder, channel, primary_data, data_size, &session_state);
        if (session_state & RMT_ENCODING_COMPLETE) {
            enc->state = 1;
        }
        if (session_state & RMT_ENCODING_MEM_FULL) {
            state |= RMT_ENCODING_MEM_FULL;
            goto out;
        }
    // fall-through
    case 1: // send reset code
        encoded_symbols += copy_encoder->encode(copy_encoder, channel, &enc->reset_code,
                                                 sizeof(enc->reset_code), &session_state);
        if (session_state & RMT_ENCODING_COMPLETE) {
            enc->state = RMT_ENCODING_RESET;
            state |= RMT_ENCODING_COMPLETE;
        }
        if (session_state & RMT_ENCODING_MEM_FULL) {
            state |= RMT_ENCODING_MEM_FULL;
            goto out;
        }
    }
out:
    *ret_state = state;
    return encoded_symbols;
}

static esp_err_t sat_led_del_encoder(rmt_encoder_t *encoder)
{
    sat_led_rmt_encoder_t *enc = __containerof(encoder, sat_led_rmt_encoder_t, base);
    rmt_del_encoder(enc->bytes_encoder);
    rmt_del_encoder(enc->copy_encoder);
    free(enc);
    return ESP_OK;
}

static esp_err_t sat_led_reset_encoder(rmt_encoder_t *encoder)
{
    sat_led_rmt_encoder_t *enc = __containerof(encoder, sat_led_rmt_encoder_t, base);
    rmt_encoder_reset(enc->bytes_encoder);
    rmt_encoder_reset(enc->copy_encoder);
    enc->state = RMT_ENCODING_RESET;
    return ESP_OK;
}

esp_err_t sat_led_new_encoder(const sat_led_encoder_config_t *config, rmt_encoder_handle_t *ret_encoder)
{
    esp_err_t ret = ESP_OK;
    sat_led_rmt_encoder_t *enc = NULL;
    ESP_GOTO_ON_FALSE(config && ret_encoder, ESP_ERR_INVALID_ARG, err, TAG, "invalid argument");
    enc = rmt_alloc_encoder_mem(sizeof(sat_led_rmt_encoder_t));
    ESP_GOTO_ON_FALSE(enc, ESP_ERR_NO_MEM, err, TAG, "no mem for led encoder");
    enc->base.encode = sat_led_encode;
    enc->base.del = sat_led_del_encoder;
    enc->base.reset = sat_led_reset_encoder;
    rmt_bytes_encoder_config_t bytes_encoder_config = {
        .bit0 = {
            .level0 = 1,
            .duration0 = 0.3 * config->resolution / 1000000, // T0H=0.3us
            .level1 = 0,
            .duration1 = 0.9 * config->resolution / 1000000, // T0L=0.9us
        },
        .bit1 = {
            .level0 = 1,
            .duration0 = 0.9 * config->resolution / 1000000, // T1H=0.9us
            .level1 = 0,
            .duration1 = 0.3 * config->resolution / 1000000, // T1L=0.3us
        },
        .flags.msb_first = 1, // WS2812 bit order: G7..G0 R7..R0 B7..B0
    };
    ESP_GOTO_ON_ERROR(rmt_new_bytes_encoder(&bytes_encoder_config, &enc->bytes_encoder), err, TAG, "bytes encoder");
    rmt_copy_encoder_config_t copy_encoder_config = {};
    ESP_GOTO_ON_ERROR(rmt_new_copy_encoder(&copy_encoder_config, &enc->copy_encoder), err, TAG, "copy encoder");

    uint32_t reset_ticks = config->resolution / 1000000 * 50 / 2; // >=50us reset/latch
    enc->reset_code = (rmt_symbol_word_t) {
        .level0 = 0, .duration0 = reset_ticks,
        .level1 = 0, .duration1 = reset_ticks,
    };
    *ret_encoder = &enc->base;
    return ESP_OK;
err:
    if (enc) {
        if (enc->bytes_encoder) rmt_del_encoder(enc->bytes_encoder);
        if (enc->copy_encoder) rmt_del_encoder(enc->copy_encoder);
        free(enc);
    }
    return ret;
}

#endif // BUILD_SATELLITE
