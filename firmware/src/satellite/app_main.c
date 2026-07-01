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
#include "ota.h"
#include "esp_system.h"

static const char *TAG = "satellite";
static device_config_t s_cfg;
static volatile uint32_t s_dropped;
static volatile bool s_reboot_pending;

// Report OTA progress back to the primary over SPI (target = our radio id).
static void sat_send_ota_status(void)
{
    uint8_t p[11];
    size_t n = 0;
    p[n++] = s_cfg.radio_id;
    p[n++] = ota_state();
    uint32_t r = ota_received(), t = ota_total();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(r >> (8 * i));
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(t >> (8 * i));
    p[n++] = ota_err();
    uint8_t out[ZB_MAX_TX];
    size_t m = zb_encode(MSG_OTA_STATUS, p, (uint16_t)n, out, sizeof(out));
    if (m) spi_slave_send(out, m);
}

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
    // OTA over SPI — the satellite IS the target, so the target byte is ignored.
    case CMD_OTA_BEGIN:
        if (len >= 9) {
            uint32_t total = (uint32_t)p[1] | (p[2] << 8) | (p[3] << 16) | ((uint32_t)p[4] << 24);
            uint32_t crc   = (uint32_t)p[5] | (p[6] << 8) | (p[7] << 16) | ((uint32_t)p[8] << 24);
            radio_capture_stop();
            ota_begin(total, crc);
            sat_send_ota_status();
        }
        break;
    case CMD_OTA_DATA:
        if (len >= 5) { ota_write(&p[5], len - 5); sat_send_ota_status(); }
        break;
    case CMD_OTA_END:
        if (ota_end() == ESP_OK) s_reboot_pending = true;
        sat_send_ota_status();
        break;
    case CMD_OTA_ABORT:
        ota_abort();
        sat_send_ota_status();
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
        if (s_reboot_pending) {   // reboot into the freshly-flashed slot
            vTaskDelay(pdMS_TO_TICKS(400));
            esp_restart();
        }
    }
}

#endif // BUILD_SATELLITE
