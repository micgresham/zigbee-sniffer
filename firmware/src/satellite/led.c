// led.c — see led.h. Drives the SuperMini's onboard single WS2812 (GPIO8) over
// RMT. GPIO8 is an ESP32-C6 strapping pin, but strapping is only latched at
// power-on reset — driving it as a normal output afterward (as the board's own
// onboard-LED wiring assumes) is safe and standard for this board.
#if defined(BUILD_SATELLITE)

#include "led.h"
#include "led_encoder.h"
#include "proto.h"
#include "driver/rmt_tx.h"
#include "esp_log.h"
#include <string.h>

#define SAT_LED_GPIO        8
#define SAT_LED_RES_HZ      10000000 // 10 MHz -> 1 tick = 0.1us (WS2812 needs ~0.3/0.9us precision)
#define SAT_LED_BRIGHTNESS  15       // dim status indicator, not a work light (0..100 scale below)

static const char *TAG = "sat-led";
static rmt_channel_handle_t s_chan;
static rmt_encoder_handle_t s_encoder;

// Standard HSV -> RGB (h: 0..359, s/v: 0..100).
static void hsv2rgb(uint32_t h, uint32_t s, uint32_t v, uint8_t *r, uint8_t *g, uint8_t *b)
{
    h %= 360;
    uint32_t rgb_max = v * 255 / 100;
    uint32_t rgb_min = rgb_max * (100 - s) / 100;
    uint32_t i = h / 60;
    uint32_t diff = h % 60;
    uint32_t rgb_adj = (rgb_max - rgb_min) * diff / 60;
    switch (i) {
    case 0:  *r = rgb_max;         *g = rgb_min + rgb_adj; *b = rgb_min;         break;
    case 1:  *r = rgb_max - rgb_adj; *g = rgb_max;         *b = rgb_min;         break;
    case 2:  *r = rgb_min;         *g = rgb_max;           *b = rgb_min + rgb_adj; break;
    case 3:  *r = rgb_min;         *g = rgb_max - rgb_adj; *b = rgb_max;         break;
    case 4:  *r = rgb_min + rgb_adj; *g = rgb_min;         *b = rgb_max;         break;
    default: *r = rgb_max;         *g = rgb_min;           *b = rgb_max - rgb_adj; break;
    }
}

// Spread the 16 Zigbee channels (11..26) evenly around the hue wheel so each
// is a distinct, stable color.
static void channel_color(uint8_t channel, uint8_t *r, uint8_t *g, uint8_t *b)
{
    if (channel < ZB_CHANNEL_MIN) channel = ZB_CHANNEL_MIN;
    if (channel > ZB_CHANNEL_MAX) channel = ZB_CHANNEL_MAX;
    uint32_t span = ZB_CHANNEL_MAX - ZB_CHANNEL_MIN + 1; // 16
    uint32_t hue = ((uint32_t)(channel - ZB_CHANNEL_MIN) * 360) / span;
    hsv2rgb(hue, 100, SAT_LED_BRIGHTNESS, r, g, b);
}

void sat_led_init(void)
{
    rmt_tx_channel_config_t tx_cfg = {
        .clk_src = RMT_CLK_SRC_DEFAULT,
        .gpio_num = SAT_LED_GPIO,
        .mem_block_symbols = 64,
        .resolution_hz = SAT_LED_RES_HZ,
        .trans_queue_depth = 2,
    };
    if (rmt_new_tx_channel(&tx_cfg, &s_chan) != ESP_OK) {
        ESP_LOGW(TAG, "RMT channel init failed — status LED disabled");
        return;
    }
    sat_led_encoder_config_t enc_cfg = { .resolution = SAT_LED_RES_HZ };
    if (sat_led_new_encoder(&enc_cfg, &s_encoder) != ESP_OK || rmt_enable(s_chan) != ESP_OK) {
        ESP_LOGW(TAG, "RMT encoder/enable failed — status LED disabled");
        s_chan = NULL;
        return;
    }
}

void sat_led_set_channel(uint8_t channel)
{
    if (!s_chan || !s_encoder) return;
    uint8_t r, g, b;
    channel_color(channel, &r, &g, &b);
    uint8_t grb[3] = { g, r, b }; // WS2812 wire order: G, R, B
    rmt_transmit_config_t tx_config = { .loop_count = 0 };
    rmt_transmit(s_chan, s_encoder, grb, sizeof(grb), &tx_config);
}

#endif // BUILD_SATELLITE
