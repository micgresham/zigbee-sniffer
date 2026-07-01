// app_main.c (standalone build) — WiFi + on-device diagnostic web UI.
//
// IMPORTANT ARCHITECTURE: the standalone *primary* does NOT run its own 802.15.4
// radio. WiFi and 802.15.4 share one radio/antenna on the C6, and running
// promiscuous capture alongside the SoftAP starves WiFi so clients can't even
// associate. So the primary keeps its radio OFF (WiFi gets it all) and obtains
// all capture data from one or more SPI **satellites** (separate C6 radios).
// Standalone therefore REQUIRES at least one satellite. See docs/hardware.md and
// docs/multi-radio.md.
#if defined(BUILD_STANDALONE)

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_log.h"
#include "esp_timer.h"

#include "proto.h"
#include "codec.h"
#include "config_nvs.h"
#include "incidents.h"
#include "radio_capture.h"
#include "wifi_ap.h"
#include "web_server.h"
#include "spi_master.h"

static const char *TAG = "standalone";

static device_config_t s_cfg;
static volatile uint8_t s_mode;
static uint64_t s_boot_us;
static volatile uint32_t s_sat_frames;   // frames received from satellites

static void bcast(uint8_t type, const uint8_t *payload, uint16_t len)
{
    uint8_t out[ZB_MAX_TX];
    size_t n = zb_encode(type, payload, len, out, sizeof(out));
    if (n) web_server_broadcast(out, n);
}

static void send_status(void)
{
    uint8_t p[32]; size_t n = 0;
    uint32_t uptime = (uint32_t)((esp_timer_get_time() - s_boot_us) / 1000000ULL);
    p[n++] = s_cfg.radio_id; p[n++] = s_mode; p[n++] = s_cfg.channel;
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(s_cfg.hop_mask >> (8 * i));
    p[n++] = (uint8_t)(s_cfg.hop_dwell_ms & 0xFF);
    p[n++] = (uint8_t)(s_cfg.hop_dwell_ms >> 8);
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(uptime >> (8 * i));
    uint32_t cap = s_sat_frames;
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(cap >> (8 * i));
    for (int i = 0; i < 4; i++) p[n++] = 0;  // dropped_crc (reserved)
    for (int i = 0; i < 4; i++) p[n++] = 0;  // dropped_buf (satellite-side)
    p[n++] = FW_MAJOR; p[n++] = FW_MINOR;
    bcast(MSG_STATUS, p, (uint16_t)n);
}

// An incident was raised on-device: push it live to any connected browser.
static void incident_cb(const char *json, size_t len)
{
    bcast(MSG_INCIDENT, (const uint8_t *)json, (uint16_t)len);
}

// A frame arrived from an SPI satellite radio: feed incidents + forward to the UI.
static void satellite_frame_cb(uint8_t radio_id, const captured_frame_t *cf)
{
    uint8_t out[ZB_MAX_TX];
    s_sat_frames++;
    incidents_note_frame(cf);
    size_t n = zb_encode_captured(radio_id, cf, out, sizeof(out));
    if (n) web_server_broadcast(out, n);
}

// Commands arriving from the browser WebSocket. Channel changes are forwarded to
// the satellite radios (the primary has no radio of its own).
static void on_command(uint8_t type, const uint8_t *p, uint16_t len)
{
    switch (type) {
    case CMD_SET_CHANNEL:
        if (len >= 1 && p[0] >= ZB_CHANNEL_MIN && p[0] <= ZB_CHANNEL_MAX) {
            s_cfg.channel = p[0];
            config_save(&s_cfg);
            spi_master_set_channel(p[0]);   // tell the satellites
        }
        break;
    case CMD_SET_MODE: if (len >= 1) s_mode = p[0]; break;
    case CMD_SET_KEY:
        if (len >= 17 && p[0]) { s_cfg.key_present = true; memcpy(s_cfg.nwk_key, &p[1], 16); config_save(&s_cfg); }
        break;
    default: break;
    }
}

void app_main(void)
{
    s_boot_us = esp_timer_get_time();
    config_load(&s_cfg);
    s_mode = s_cfg.mode;

    const char *ip = wifi_start(&s_cfg);

    // Init the local 802.15.4 PHY then immediately park it asleep. The PHY init
    // satisfies the C6 sleep-retention dependency (an enabled-but-uninitialised
    // 802.15.4 causes an "Illegal dependency" panic at boot), while parking it
    // keeps the radio off-air so it doesn't starve the WiFi AP. The primary does
    // NOT capture locally — capture comes from SPI satellites.
    radio_capture_init(s_cfg.channel, s_cfg.radio_id);
    radio_capture_stop();

    incidents_init(incident_cb);
    web_server_start(on_command);

    // Standalone REQUIRES at least one SPI satellite for capture; the primary's
    // own radio is intentionally never enabled (frees the radio for WiFi).
    spi_master_init(satellite_frame_cb);
    spi_master_set_channel(s_cfg.channel);

    ESP_LOGW(TAG, "standalone primary: own 802.15.4 radio OFF (WiFi-only).");
    ESP_LOGI(TAG, "UI at http://%s — capture comes from SPI satellite(s), channel %u",
             ip, s_cfg.channel);

    uint64_t last_status = esp_timer_get_time();
    uint64_t last_tick = last_status;
    for (;;) {
        vTaskDelay(pdMS_TO_TICKS(50));   // frames arrive async via satellite_frame_cb
        uint64_t now = esp_timer_get_time();
        if (now - last_status >= 1000000ULL) { last_status = now; send_status(); }
        if (now - last_tick >= 5000000ULL) { last_tick = now; incidents_tick(s_cfg.channel); }
    }
}

#endif // BUILD_STANDALONE
