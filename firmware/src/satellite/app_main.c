// app_main.c (satellite build) — dumb capture-and-forward over SPI to the primary.
//
// Captures 802.15.4 frames and streams them (same binary framing) to the primary
// over an SPI-slave link. Accepts a few commands back (set channel, radio id).
// See docs/multi-radio.md and docs/carrier.md.
#if defined(BUILD_SATELLITE)

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_log.h"

#include "proto.h"
#include "codec.h"
#include "config_nvs.h"
#include "radio_capture.h"
#include "transport_spi.h"

static const char *TAG = "satellite";
static device_config_t s_cfg;
static volatile uint32_t s_dropped;

// Commands from the primary over SPI.
static void on_command(uint8_t type, const uint8_t *p, uint16_t len)
{
    switch (type) {
    case CMD_SET_CHANNEL:
        if (len >= 1) { radio_capture_set_channel(p[0]); s_cfg.channel = p[0]; }
        break;
    case CMD_SET_RADIO_ID:
        if (len >= 1) { s_cfg.radio_id = p[0]; config_save(&s_cfg); }
        break;
    default:
        break;
    }
}

void app_main(void)
{
    config_load(&s_cfg);
    if (s_cfg.radio_id == 0) s_cfg.radio_id = 1;   // satellites are 1..3

    spi_slave_transport_init(on_command);
    radio_capture_init(s_cfg.channel, s_cfg.radio_id);
    radio_capture_start();
    ESP_LOGI(TAG, "satellite up: radio_id=%u ch=%u (SPI forward)",
             s_cfg.radio_id, s_cfg.channel);

    uint8_t out[ZB_MAX_TX];
    captured_frame_t cf;
    for (;;) {
        if (radio_capture_recv(&cf, 50)) {
            size_t n = zb_encode_captured(s_cfg.radio_id, &cf, out, sizeof(out));
            if (n && !spi_slave_send(out, n)) s_dropped++;
        }
    }
}

#endif // BUILD_SATELLITE
