// app_main.c (usb-sniffer build) — the "true sniffer".
//
// Streams captured 802.15.4 frames and energy-detect samples to the host over
// USB Serial/JTAG using the shared framing protocol, and accepts commands
// (set channel, mode, hop, ED scan...) back from the host.
//
// All build dirs compile in every env (see src/CMakeLists.txt); this guard
// keeps only the selected build's app_main() active.
#if defined(BUILD_USB_SNIFFER)

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_log.h"
#include "esp_timer.h"

#include "proto.h"
#include "codec.h"
#include "config_nvs.h"
#include "radio_capture.h"
#include "esp_ieee802154.h"
#include "ed_scan.h"
#include "probe.h"
#include "beacon.h"
#include "ota.h"
#include "standalone/spi_master.h"
#include "transport_usb.h"
#include "esp_system.h"

static const char *TAG = "usb-sniffer";

static device_config_t s_cfg;
static volatile uint8_t s_mode;
static volatile uint32_t s_hop_mask;
static volatile uint16_t s_hop_dwell_ms;
static uint64_t s_boot_us;

// --- helpers ---------------------------------------------------------------

static void send_frame(uint8_t type, const uint8_t *payload, uint16_t len)
{
    uint8_t out[ZB_MAX_TX];
    size_t n = zb_encode(type, payload, len, out, sizeof(out));
    if (n) transport_usb_write(out, n);
}

static void send_log(const char *msg)
{
    send_frame(MSG_LOG, (const uint8_t *)msg, (uint16_t)strlen(msg));
}

static void send_ack(uint8_t cmd_type, uint8_t result)
{
    uint8_t p[2] = { cmd_type, result };
    send_frame(MSG_ACK, p, 2);
}

static void send_status(void)
{
    uint8_t p[32];
    size_t n = 0;
    uint32_t uptime = (uint32_t)((esp_timer_get_time() - s_boot_us) / 1000000ULL);
    p[n++] = s_cfg.radio_id;
    p[n++] = s_mode;
    p[n++] = radio_capture_get_channel();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(s_hop_mask >> (8 * i));
    p[n++] = (uint8_t)(s_hop_dwell_ms & 0xFF);
    p[n++] = (uint8_t)(s_hop_dwell_ms >> 8);
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(uptime >> (8 * i));
    uint32_t cap = radio_capture_count();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(cap >> (8 * i));
    uint32_t dcrc = 0;  // hardware filters CRC in promiscuous; reserved
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(dcrc >> (8 * i));
    uint32_t dbuf = radio_capture_dropped_buf();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(dbuf >> (8 * i));
    p[n++] = FW_MAJOR;
    p[n++] = FW_MINOR;
    send_frame(MSG_STATUS, p, (uint16_t)n);
}

static void ed_cb(const ed_sample_t *s, void *ctx)
{
    (void)ctx;
    uint8_t p[16];
    size_t n = 0;
    p[n++] = s_cfg.radio_id;
    p[n++] = s->channel;
    for (int i = 0; i < 8; i++) p[n++] = (uint8_t)(s->timestamp >> (8 * i));
    p[n++] = (uint8_t)s->ed_dbm;
    p[n++] = (uint8_t)(s->sweep_id & 0xFF);
    p[n++] = (uint8_t)(s->sweep_id >> 8);
    send_frame(MSG_ED_RESULT, p, (uint16_t)n);
}

// Active-probe result → MSG_PROBE_RESULT.
static void probe_cb(uint16_t target, bool acked, int8_t rssi, uint8_t lqi)
{
    uint8_t p[6];
    p[0] = s_cfg.radio_id;
    p[1] = target & 0xFF;
    p[2] = target >> 8;
    p[3] = acked ? 1 : 0;
    p[4] = (uint8_t)rssi;
    p[5] = lqi;
    send_frame(MSG_PROBE_RESULT, p, sizeof(p));
}

// OTA progress → MSG_OTA_STATUS (target 0 = this C6).
static void send_ota_status(uint8_t target)
{
    uint8_t p[11];
    size_t n = 0;
    p[n++] = target;
    p[n++] = ota_state();
    uint32_t r = ota_received(), t = ota_total();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(r >> (8 * i));
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(t >> (8 * i));
    p[n++] = ota_err();
    send_frame(MSG_OTA_STATUS, p, (uint16_t)n);
}

static volatile bool s_reboot_pending;

// If a satellite's radio_id collides with this C6's own, tag it with the high
// bit so the host can still tell the two apart instead of silently merging
// them into one radio (see RADIO_ID_COLLISION_BIT in proto.h). Logs once.
static uint8_t tag_if_colliding(uint8_t radio_id)
{
    if (radio_id != s_cfg.radio_id) return radio_id;
    static bool warned;
    if (!warned) {
        warned = true;
        ESP_LOGW(TAG, "satellite radio_id=%u collides with this primary's own id=%u — "
                 "reassign one via the dashboard's Radio ID field", radio_id, s_cfg.radio_id);
    }
    return radio_id | RADIO_ID_COLLISION_BIT;
}

// Relay a satellite's non-frame messages (arriving over SPI) up to the host,
// opaquely — OTA progress, and its ~1Hz STATUS heartbeat (sent independent of
// captured frames so a satellite on a silent channel is still discoverable;
// see sat_send_status() in satellite/app_main.c). The host tells radios apart
// by the radio_id already embedded in the payload.
static void msg_from_sat(uint8_t type, const uint8_t *pl, uint16_t len)
{
    if ((type == MSG_STATUS || type == MSG_ED_RESULT || type == MSG_PROBE_RESULT) && len >= 1) {
        // All three carry the satellite's radio_id in payload[0]; tag it if it
        // collides with the primary's own id so the host keeps them distinct.
        // ED sweeps (spectrum role) and probes (tester role) now originate on
        // satellites too, so their results must be relayed up like STATUS was.
        uint8_t fixed[64];
        if (len > sizeof(fixed)) return;
        memcpy(fixed, pl, len);
        fixed[0] = tag_if_colliding(fixed[0]);
        send_frame(type, fixed, len);
    } else if (type == MSG_OTA_STATUS) {
        send_frame(type, pl, len);
    }
}

// A frame arrived from an SPI satellite radio: forward it to the host over USB,
// same framing as our own captures — the host tells them apart by radio_id
// (0 = this C6's own radio, 1..3 = satellites). See docs/multi-radio.md.
static void satellite_frame_cb(uint8_t radio_id, const captured_frame_t *cf)
{
    static bool logged;
    if (!logged) { logged = true; ESP_LOGI(TAG, "first frame relayed from satellite radio_id=%u — SPI link is up", radio_id); }
    radio_id = tag_if_colliding(radio_id);
    uint8_t out[ZB_MAX_TX];
    size_t n = zb_encode_captured(radio_id, cf, out, sizeof(out));
    if (n) transport_usb_write(out, n);
}

// Re-frame a command and forward it to satellite (target-1) over SPI.
static void relay_to_satellite(uint8_t target, uint8_t type, const uint8_t *p, uint16_t len)
{
    if (target < 1 || target > PRI_SAT_COUNT) return;
    uint8_t out[ZB_MAX_TX];
    size_t n = zb_encode(type, p, len, out, sizeof(out));
    if (n) spi_master_send_to(target - 1, out, n);
}

// CMD_SAT_SET_CHANNEL/CMD_SAT_START/CMD_SAT_STOP arrive as target(1) [+ inner
// args]; strip the target byte and relay the plain inner command (which a
// satellite already understands) via relay_to_satellite().
static void sat_cmd_relay(uint8_t sat_type, const uint8_t *p, uint16_t len)
{
    if (len < 1) return;
    uint8_t target = p[0];
    uint8_t inner_type;
    switch (sat_type) {
    case CMD_SAT_SET_CHANNEL: inner_type = CMD_SET_CHANNEL; break;
    case CMD_SAT_START:       inner_type = CMD_START; break;
    case CMD_SAT_STOP:        inner_type = CMD_STOP; break;
    default: return;
    }
    relay_to_satellite(target, inner_type, p + 1, len - 1);
}

// --- command handling ------------------------------------------------------

static void handle_command(uint8_t type, const uint8_t *p, uint16_t len)
{
    switch (type) {
    case CMD_SET_CHANNEL:
        // This C6's own radio ONLY — does not touch any SPI satellite. A global,
        // all-radios channel change explicitly relays CMD_SAT_SET_CHANNEL to each
        // satellite too (see /api/set_channel on the host); this command alone
        // must stay scoped to the local radio so a per-radio channel-set (e.g.
        // /api/radio_channel targeting just this C6) doesn't leak to satellites.
        if (len >= 1) {
            radio_lock();
            radio_capture_set_channel(p[0]);
            radio_unlock();
            s_cfg.channel = p[0]; config_save(&s_cfg);
        }
        send_ack(type, 0);
        break;
    case CMD_SET_MODE:
        // Mode is just a flag the main loop reads — no radio HAL here, so no lock
        // is needed. This is deliberately cheap so a mode change is honored even
        // while the main loop holds the radio for a sweep.
        if (len >= 1) s_mode = p[0];
        send_ack(type, 0);
        break;
    case CMD_START:
        radio_lock();
        radio_capture_start();
        radio_unlock();
        send_ack(type, 0);
        break;
    case CMD_STOP:
        radio_lock();
        radio_capture_stop();
        radio_unlock();
        send_ack(type, 0);
        break;
    case CMD_SET_HOP:
        if (len >= 6) {
            s_hop_mask = (uint32_t)p[0] | (p[1] << 8) | (p[2] << 16) | ((uint32_t)p[3] << 24);
            s_hop_dwell_ms = (uint16_t)p[4] | (p[5] << 8);
        }
        send_ack(type, 0);
        break;
    case CMD_ED_SCAN:
        if (len >= 6) {
            uint32_t mask = (uint32_t)p[0] | (p[1] << 8) | (p[2] << 16) | ((uint32_t)p[3] << 24);
            uint32_t dwell_ms = (uint16_t)p[4] | (p[5] << 8);
            radio_lock();  // don't collide with a continuous sweep on the main loop
            ed_sweep(mask, dwell_ms * 1000, ed_cb, NULL);
            radio_unlock();
        }
        send_ack(type, 0);
        break;
    case CMD_SET_KEY:
        if (len >= 1 && p[0] && len >= 17) {
            s_cfg.key_present = true;
            memcpy(s_cfg.nwk_key, &p[1], 16);
            config_save(&s_cfg);
        }
        send_ack(type, 0);
        break;
    // No CMD_SET_RADIO_ID here, deliberately: this C6's own radio_id is fixed
    // at 0 (see app_main()) — the whole SPI-satellite addressing scheme
    // depends on primary=0, satellite=slot+1 (see spi_master.c) never
    // drifting, and this command used to let it drift, colliding with a
    // satellite and making both id-based lookups ambiguous. Falls through to
    // "unknown command" below.
    case CMD_PROBE:
        if (len >= 4) {
            uint16_t target = (uint16_t)p[0] | (p[1] << 8);
            uint16_t pan    = (uint16_t)p[2] | (p[3] << 8);
            radio_lock();
            probe_send(target, pan);
            radio_unlock();
        }
        send_ack(type, 0);
        break;
    case CMD_BEACON_REQ:
        radio_lock();
        beacon_request_send();
        radio_unlock();
        send_ack(type, 0);
        break;
    case CMD_TX_RAW:
        // Transmit a host-built raw MPDU (e.g. an active ZDO interrogation). The
        // radio appends the 2-byte FCS, so the PHY length is len + 2.
        if (len >= 3 && len <= 125) {
            radio_lock();
            esp_ieee802154_set_rx_when_idle(true);
            esp_ieee802154_receive();
            uint8_t f[1 + 128];
            f[0] = len + 2;
            memcpy(&f[1], p, len);
            esp_ieee802154_transmit(f, false);
            radio_unlock();
        }
        send_ack(type, 0);
        break;
    case CMD_OTA_BEGIN:
        if (len >= 9) {
            uint8_t target = p[0];
            uint32_t total = (uint32_t)p[1] | (p[2] << 8) | (p[3] << 16) | ((uint32_t)p[4] << 24);
            uint32_t crc   = (uint32_t)p[5] | (p[6] << 8) | (p[7] << 16) | ((uint32_t)p[8] << 24);
            if (target == 0) {
                radio_lock();
                radio_capture_stop();  // free the radio during flash writes
                radio_unlock();
                ota_begin(total, crc);
                send_ota_status(target);
            } else {
                relay_to_satellite(target, type, p, len); // forward to a satellite over SPI
            }
        }
        break;
    case CMD_OTA_DATA:
        if (len >= 5) {
            uint8_t target = p[0];
            if (target == 0) { ota_write(&p[5], len - 5); send_ota_status(target); }
            else relay_to_satellite(target, type, p, len);
        }
        break;
    case CMD_OTA_END:
        if (len >= 1) {
            uint8_t target = p[0];
            if (target == 0) { if (ota_end() == ESP_OK) s_reboot_pending = true; send_ota_status(target); }
            else relay_to_satellite(target, type, p, len);
        }
        break;
    case CMD_OTA_ABORT:
        if (len >= 1) {
            uint8_t target = p[0];
            if (target == 0) { ota_abort(); send_ota_status(target); }
            else relay_to_satellite(target, type, p, len);
        }
        break;
    case CMD_SAT_SET_CHANNEL:
    case CMD_SAT_START:
    case CMD_SAT_STOP:
        sat_cmd_relay(type, p, len);
        break;
    case CMD_SAT_RELAY:
        // Generic relay: p[0]=target, p[1..]=a complete inner zb frame. Forward
        // the inner frame verbatim over SPI — no re-encoding, no per-command
        // mapping, so any command reaches the satellite (see CMD_SAT_RELAY in
        // proto.h). This is how satellites do ED/hop/probe/beacon/raw-TX.
        if (len >= 1) {
            uint8_t target = p[0];
            if (target >= 1 && target <= PRI_SAT_COUNT && len > 1) {
                spi_master_send_to(target - 1, p + 1, len - 1);
            }
        }
        break;
    case CMD_GET_STATUS:
        send_status();
        break;
    default:
        send_ack(type, 1);  // unknown command
        break;
    }
}

static void command_task(void *arg)
{
    (void)arg;
    static zb_decoder_t dec;   // ~2 KB — keep off the task stack
    zb_decoder_init(&dec);
    uint8_t buf[128];
    for (;;) {
        int n = transport_usb_read(buf, sizeof(buf), 50);
        for (int i = 0; i < n; i++) {
            uint8_t type; const uint8_t *pl; uint16_t pl_len;
            if (zb_decoder_push(&dec, buf[i], &type, &pl, &pl_len)) {
                handle_command(type, pl, pl_len);
            }
        }
    }
}

void app_main(void)
{
    s_boot_us = esp_timer_get_time();
    config_load(&s_cfg);
    // Fixed at 0, always — see the CMD_SET_RADIO_ID removal note above. Forced
    // here too in case NVS holds a stale non-zero value from before that fix.
    if (s_cfg.radio_id != 0) { s_cfg.radio_id = 0; config_save(&s_cfg); }
    s_mode = s_cfg.mode;
    s_hop_mask = s_cfg.hop_mask;
    s_hop_dwell_ms = s_cfg.hop_dwell_ms;

    transport_usb_init();
    radio_capture_init(s_cfg.channel, s_cfg.radio_id);
    radio_capture_start();
    probe_init(probe_cb);

    // SPI satellites are optional here (unlike standalone, which requires one) —
    // if none are wired, DATA_READY just never goes high and this costs a quiet
    // poll loop. Always up so satellite captures/status/OTA merge in from boot.
    // Deliberately no spi_master_set_channel() push here: each satellite keeps
    // its own independently-persisted channel (see config_save in
    // satellite/app_main.c) across a primary reboot/reconnect. Pushing this
    // C6's own saved channel to every satellite here used to silently stomp
    // whatever channel each one had actually been set to — the exact bug where
    // every radio comes back up on the primary's last channel after a restart.
    spi_master_init(satellite_frame_cb);
    spi_master_set_msg_cb(msg_from_sat);

    ESP_LOGI(TAG, "usb-sniffer up: ch=%u radio=%u", s_cfg.channel, s_cfg.radio_id);
    send_log("zigbee-sniffer usb-sniffer ready");

    xTaskCreate(command_task, "cmd", 4096, NULL, 5, NULL);

    uint8_t out[ZB_MAX_TX];
    captured_frame_t cf;
    uint64_t last_status = esp_timer_get_time();
    uint64_t last_hop = last_status;
    uint8_t hop_idx = 0;

    for (;;) {
        // Drain captured frames (CAPTURE / CAPTURE_PLUS_ED modes).
        if (s_mode == MODE_CAPTURE || s_mode == MODE_CAPTURE_PLUS_ED) {
            while (radio_capture_recv(&cf, 5)) {
                size_t n = zb_encode_captured(s_cfg.radio_id, &cf, out, sizeof(out));
                if (n) transport_usb_write(out, n);
            }
        } else if (s_mode == MODE_ED_SWEEP) {
            // Sweep under the radio lock so a command-task op (a one-shot scan,
            // probe/beacon TX, channel change) can't drive the HAL concurrently
            // and hang it. The lock is released each iteration, so a mode change
            // (which only sets s_mode, no lock needed) is honored on the next
            // pass and the sweep stops promptly.
            radio_lock();
            ed_sweep(s_hop_mask, (s_hop_dwell_ms ? s_hop_dwell_ms : 5) * 1000, ed_cb, NULL);
            radio_unlock();
            vTaskDelay(pdMS_TO_TICKS(50));
        } else {
            vTaskDelay(pdMS_TO_TICKS(20));
        }

        uint64_t now = esp_timer_get_time();

        // Channel hopping during capture.
        if (s_hop_dwell_ms && (s_mode == MODE_CAPTURE) &&
            (now - last_hop) / 1000 >= s_hop_dwell_ms) {
            last_hop = now;
            radio_lock();
            for (int tries = 0; tries < 16; tries++) {
                hop_idx = (hop_idx + 1) % 16;
                if (s_hop_mask & (1u << hop_idx)) {
                    radio_capture_set_channel(ZB_CHANNEL_MIN + hop_idx);
                    break;
                }
            }
            radio_unlock();
        }

        // Periodic status heartbeat (~1 Hz).
        if (now - last_status >= 1000000ULL) {
            last_status = now;
            send_status();
        }

        // Reboot after an OTA completes (let the final status message flush).
        if (s_reboot_pending) {
            vTaskDelay(pdMS_TO_TICKS(400));
            esp_restart();
        }
    }
}

#endif // BUILD_USB_SNIFFER
