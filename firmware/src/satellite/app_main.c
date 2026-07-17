// app_main.c (satellite build) — dumb capture-and-forward over SPI to the primary.
//
// Captures 802.15.4 frames and streams them (same binary framing) to the primary
// over an SPI-slave link. Accepts a few commands back (set channel, radio id).
// See docs/multi-radio.md and docs/carrier.md.
#if defined(BUILD_SATELLITE)

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include "esp_log.h"

#include "proto.h"
#include "codec.h"
#include "config_nvs.h"
#include "radio_capture.h"
#include "transport_spi.h"
#include "ed_scan.h"
#include "probe.h"
#include "beacon.h"
#include "esp_ieee802154.h"
#include "ota.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "led.h"

static const char *TAG = "satellite";
static device_config_t s_cfg;
static volatile uint32_t s_dropped;
static volatile bool s_reboot_pending;
// Operating mode + hop set, mirroring the primary (usb-sniffer) — a satellite
// is now a full radio, not capture-only: it can run ED sweeps (spectrum role),
// channel-hop (hopper role), and TX probes/beacons/raw (tester role). Set by
// relayed CMD_SET_MODE / CMD_SET_HOP; read by the main loop below.
static volatile uint8_t  s_mode;
static volatile uint32_t s_hop_mask;
static volatile uint16_t s_hop_dwell_ms;

// Periodic liveness/status heartbeat (~1Hz), independent of captured frames —
// without this, a satellite on a quiet channel (or mid-debug with no traffic)
// never asserts DATA_READY at all, so the primary never polls it and it's
// invisible to the host regardless of whether the SPI wiring is fine. Reuses
// the STATUS wire format; the primary passes it through opaquely to the host
// (see msg_from_sat() in usb-sniffer/app_main.c), which tracks it as its own
// radio the same way it already does for the primary's own STATUS.
static void sat_send_status(uint64_t boot_us)
{
    uint8_t p[27];
    size_t n = 0;
    uint32_t uptime = (uint32_t)((esp_timer_get_time() - boot_us) / 1000000ULL);
    p[n++] = s_cfg.radio_id;
    p[n++] = s_mode;                          // satellites now run any mode, not just capture
    p[n++] = radio_capture_get_channel();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(s_hop_mask >> (8 * i));
    p[n++] = (uint8_t)(s_hop_dwell_ms & 0xFF);
    p[n++] = (uint8_t)(s_hop_dwell_ms >> 8);
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(uptime >> (8 * i));
    uint32_t cap = radio_capture_count();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(cap >> (8 * i));
    for (int i = 0; i < 4; i++) p[n++] = 0;   // dropped_crc: reserved (see usb-sniffer)
    uint32_t dbuf = radio_capture_dropped_buf();
    for (int i = 0; i < 4; i++) p[n++] = (uint8_t)(dbuf >> (8 * i));
    p[n++] = FW_MAJOR;
    p[n++] = FW_MINOR;
    uint8_t out[ZB_MAX_TX];
    size_t m = zb_encode(MSG_STATUS, p, (uint16_t)n, out, sizeof(out));
    if (m) spi_slave_send(out, m);
}

// Report OTA progress back to the primary over SPI (target = our radio id).
// spi_slave_send() queues into a fixed-size ring buffer and can fail (silently,
// by design — the SPI slave task calling it elsewhere can't block) if that
// ring is momentarily full. This runs on ota_task, not the SPI task, so unlike
// other callers it CAN afford to retry briefly — worth doing here because a
// dropped BEGIN/END/ABORT status is exactly how an OTA used to appear to hang
// forever with no visible error: the flash write genuinely completed, but the
// one message revealing that (or a failure) never reached the host.
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
    if (!m) return;
    for (int attempt = 0; attempt < 20; attempt++) {
        if (spi_slave_send(out, m)) return;
        vTaskDelay(pdMS_TO_TICKS(5));
    }
    ESP_LOGW(TAG, "OTA status send failed after retries (ring buffer stayed full)");
}

// ED sweep sample → MSG_ED_RESULT over SPI (same payload layout as the primary's
// ed_cb in usb-sniffer). The primary relays it up to the host opaquely; the
// radio_id in the payload attributes it to this satellite.
static void sat_ed_cb(const ed_sample_t *s, void *ctx)
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
    uint8_t out[ZB_MAX_TX];
    size_t m = zb_encode(MSG_ED_RESULT, p, (uint16_t)n, out, sizeof(out));
    if (m && !spi_slave_send(out, m)) s_dropped++;
}

// Active-probe result → MSG_PROBE_RESULT over SPI (matches the primary's probe_cb).
static void sat_probe_cb(uint16_t target, bool acked, int8_t rssi, uint8_t lqi)
{
    uint8_t p[6];
    p[0] = s_cfg.radio_id;
    p[1] = target & 0xFF;
    p[2] = target >> 8;
    p[3] = acked ? 1 : 0;
    p[4] = (uint8_t)rssi;
    p[5] = lqi;
    uint8_t out[ZB_MAX_TX];
    size_t m = zb_encode(MSG_PROBE_RESULT, p, sizeof(p), out, sizeof(out));
    if (m) spi_slave_send(out, m);
}

// OTA commands are relayed to a dedicated task instead of handled inline in
// on_command() (see ota_task() below) — ota_write() performs a synchronous
// flash write/erase (tens of ms per sector), and on_command() runs on the SPI
// task, the same task responsible for immediately re-arming the next SPI
// transaction. While a flash op blocks that task, the primary keeps sending a
// new OTA_DATA chunk every ~8ms with no flow control; the satellite's SPI
// slave isn't ready to receive them, so nearly the whole transfer was
// silently lost right after OTA_BEGIN. Decoupling receive from processing
// fixes it: the SPI task only ever does a fast, non-blocking queue send.
// 48, not 16: the host now paces itself off our own OTA_STATUS "received"
// acknowledgment (see the flow-control gate in /api/ota) instead of a blind
// fixed delay, so this no longer needs to absorb a full send-side burst on
// its own — just extra headroom against a slow flash sector erase.
#define OTA_Q_DEPTH 48
#define OTA_CMD_MAX_PAYLOAD 220
typedef struct {
    uint8_t  type;
    uint16_t len;
    uint8_t  payload[OTA_CMD_MAX_PAYLOAD];
} sat_ota_cmd_t;
static QueueHandle_t s_ota_q;

static void ota_task(void *arg)
{
    (void)arg;
    sat_ota_cmd_t cmd;
    for (;;) {
        if (xQueueReceive(s_ota_q, &cmd, portMAX_DELAY) != pdTRUE) continue;
        const uint8_t *p = cmd.payload;
        uint16_t len = cmd.len;
        switch (cmd.type) {
        case CMD_OTA_BEGIN:
            if (len >= 9) {
                uint32_t total = (uint32_t)p[1] | (p[2] << 8) | (p[3] << 16) | ((uint32_t)p[4] << 24);
                uint32_t crc   = (uint32_t)p[5] | (p[6] << 8) | (p[7] << 16) | ((uint32_t)p[8] << 24);
                radio_lock();
                radio_capture_stop();  // free the radio during flash writes
                radio_unlock();
                ota_begin(total, crc);
                sat_send_ota_status();
            }
            break;
        case CMD_OTA_DATA:
            // Every chunk gets its own status reply, unthrottled: the host
            // now waits for THIS chunk's cumulative byte count to be
            // acknowledged before sending the next one (see the /api/ota
            // confirmation gate), so nothing floods the ring buffer anymore —
            // and skipping some replies would mean the host can't tell a
            // specific chunk apart from ones after it, defeating the whole
            // point of per-chunk confirmation.
            if (len >= 5) {
                uint32_t offset = (uint32_t)p[1] | (p[2] << 8) | (p[3] << 16) | ((uint32_t)p[4] << 24);
                uint32_t have = ota_received();
                // ota_write() always appends at its own internal position
                // (s_recv), not at whatever offset the message carries — the
                // offset here is only used to detect a duplicate. If the
                // host's own confirmation reply for an already-written chunk
                // was lost (not the chunk itself, just our reply), the host
                // retries by resending that same chunk. Writing it a second
                // time would silently double-append it, shifting everything
                // after it by one chunk's length and corrupting the image.
                if (offset < have) {
                    // Already applied — just re-confirm without rewriting.
                } else if (offset > have) {
                    ESP_LOGW(TAG, "OTA data gap: expected offset %u, got %u", (unsigned)have, (unsigned)offset);
                } else {
                    ota_write(&p[5], len - 5);
                }
                sat_send_ota_status();
            }
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
}

// Queue an OTA command for ota_task(); drops it (logging once) if the queue
// is somehow still full — a flash op stuck long enough to fill 16 slots is a
// genuine stall, not something to block the SPI task waiting on.
static void ota_enqueue(uint8_t type, const uint8_t *p, uint16_t len)
{
    sat_ota_cmd_t cmd;
    cmd.type = type;
    cmd.len = len > sizeof(cmd.payload) ? (uint16_t)sizeof(cmd.payload) : len;
    memcpy(cmd.payload, p, cmd.len);
    if (xQueueSend(s_ota_q, &cmd, 0) != pdTRUE) {
        static bool warned;
        if (!warned) { warned = true; ESP_LOGW(TAG, "OTA queue full — dropping a command"); }
    }
}

// Commands from the primary over SPI.
static void on_command(uint8_t type, const uint8_t *p, uint16_t len)
{
    ESP_LOGI(TAG, "cmd 0x%02x len=%u received over SPI", type, len);
    switch (type) {
    case CMD_SET_CHANNEL:
        // radio_lock() matters here: this runs on the SPI task, a different
        // task from the main loop's radio_capture_recv() / the driver's
        // receive-done callback — an unsynchronized esp_ieee802154_set_channel()
        // racing a live receive can leave the radio not properly re-armed for
        // RX (channel "sets" but the satellite then captures nothing).
        if (len >= 1) {
            radio_lock();
            radio_capture_set_channel(p[0]);
            radio_unlock();
            s_cfg.channel = p[0]; sat_led_set_channel(p[0]);
            config_save(&s_cfg);   // survive a reboot/OTA instead of reverting to the old default
        }
        break;
    case CMD_SET_RADIO_ID:
        if (len >= 1) { s_cfg.radio_id = p[0]; config_save(&s_cfg); }
        break;
    // Relayed via CMD_SAT_START/CMD_SAT_STOP on the primary — arrives here as the
    // plain command (target byte already stripped by the primary's relay).
    case CMD_START:
        radio_lock();
        radio_capture_start();
        radio_unlock();
        break;
    case CMD_STOP:
        radio_lock();
        radio_capture_stop();
        radio_unlock();
        break;
    // Full radio command set (relayed generically via CMD_SAT_RELAY on the
    // primary), making a satellite symmetric with the primary radio.
    case CMD_SET_MODE:
        // Just a flag the main loop reads (no HAL) — cheap so a mode change is
        // honored even while the loop holds the radio for a sweep.
        if (len >= 1) s_mode = p[0];
        break;
    case CMD_SET_HOP:
        if (len >= 6) {
            s_hop_mask = (uint32_t)p[0] | (p[1] << 8) | (p[2] << 16) | ((uint32_t)p[3] << 24);
            s_hop_dwell_ms = (uint16_t)p[4] | (p[5] << 8);
        }
        break;
    case CMD_ED_SCAN:
        if (len >= 6) {
            uint32_t mask = (uint32_t)p[0] | (p[1] << 8) | (p[2] << 16) | ((uint32_t)p[3] << 24);
            uint32_t dwell_ms = (uint16_t)p[4] | (p[5] << 8);
            radio_lock();
            ed_sweep(mask, dwell_ms * 1000, sat_ed_cb, NULL);
            radio_unlock();
        }
        break;
    case CMD_PROBE:
        if (len >= 4) {
            uint16_t target = (uint16_t)p[0] | (p[1] << 8);
            uint16_t pan    = (uint16_t)p[2] | (p[3] << 8);
            radio_lock();
            probe_send(target, pan);
            radio_unlock();
        }
        break;
    case CMD_BEACON_REQ:
        radio_lock();
        beacon_request_send();
        radio_unlock();
        break;
    case CMD_TX_RAW:
        if (len >= 3 && len <= 125) {
            radio_lock();
            esp_ieee802154_set_rx_when_idle(true);
            esp_ieee802154_receive();
            uint8_t f[1 + 128];
            f[0] = len + 2;              // PHY length includes the 2-byte FCS the radio appends
            memcpy(&f[1], p, len);
            esp_ieee802154_transmit(f, false);
            radio_unlock();
        }
        break;
    // OTA over SPI — the satellite IS the target, so the target byte is
    // ignored. Handed off to ota_task() (see ota_enqueue() above) rather than
    // processed here inline.
    case CMD_OTA_BEGIN:
    case CMD_OTA_DATA:
    case CMD_OTA_END:
    case CMD_OTA_ABORT:
        ota_enqueue(type, p, len);
        break;
    default:
        break;
    }
}

void app_main(void)
{
    config_load(&s_cfg);
    if (s_cfg.radio_id == 0) s_cfg.radio_id = 1;   // satellites are 1..3
    s_mode = MODE_CAPTURE;                          // default role: sniffer
    s_hop_mask = s_cfg.hop_mask;
    s_hop_dwell_ms = s_cfg.hop_dwell_ms;

    sat_led_init();
    sat_led_set_channel(s_cfg.channel);

    s_ota_q = xQueueCreate(OTA_Q_DEPTH, sizeof(sat_ota_cmd_t));
    xTaskCreate(ota_task, "sat_ota", 4096, NULL, 5, NULL);

    spi_slave_transport_init(on_command);
    radio_capture_init(s_cfg.channel, s_cfg.radio_id);
    probe_init(sat_probe_cb);
    radio_capture_start();
    ESP_LOGI(TAG, "satellite up: radio_id=%u ch=%u (SPI forward)",
             s_cfg.radio_id, s_cfg.channel);

    uint64_t boot_us = esp_timer_get_time();
    uint8_t out[ZB_MAX_TX];
    captured_frame_t cf;
    bool logged_first_capture = false;
    uint64_t last_status = boot_us;
    uint64_t last_heartbeat = boot_us;
    uint64_t last_hop = boot_us;
    uint8_t hop_idx = 0;
    for (;;) {
        // Mode-driven, mirroring the primary's main loop: capture drains frames,
        // ED_SWEEP runs a continuous sweep, else idle. Each holds the radio lock
        // only briefly so a relayed command (mode/channel change, probe) applies
        // promptly on the next pass.
        if (s_mode == MODE_CAPTURE || s_mode == MODE_CAPTURE_PLUS_ED) {
            if (radio_capture_recv(&cf, 50)) {
                if (!logged_first_capture) {
                    logged_first_capture = true;
                    ESP_LOGI(TAG, "first frame captured locally: ch=%u len=%u rssi=%d — radio RX is live",
                             cf.channel, cf.len, cf.rssi);
                }
                size_t n = zb_encode_captured(s_cfg.radio_id, &cf, out, sizeof(out));
                if (n && !spi_slave_send(out, n)) s_dropped++;
            }
        } else if (s_mode == MODE_ED_SWEEP) {
            radio_lock();
            ed_sweep(s_hop_mask, (s_hop_dwell_ms ? s_hop_dwell_ms : 5) * 1000, sat_ed_cb, NULL);
            radio_unlock();
            vTaskDelay(pdMS_TO_TICKS(50));
        } else {
            vTaskDelay(pdMS_TO_TICKS(20));
        }
        uint64_t now = esp_timer_get_time();
        // Channel hopping during capture (hopper role).
        if (s_hop_dwell_ms && s_mode == MODE_CAPTURE &&
            (now - last_hop) / 1000 >= s_hop_dwell_ms) {
            last_hop = now;
            radio_lock();
            for (int tries = 0; tries < 16; tries++) {
                hop_idx = (hop_idx + 1) % 16;
                if (s_hop_mask & (1u << hop_idx)) {
                    radio_capture_set_channel(ZB_CHANNEL_MIN + hop_idx);
                    sat_led_set_channel(ZB_CHANNEL_MIN + hop_idx);
                    break;
                }
            }
            radio_unlock();
        }
        if (now - last_status >= 1000000ULL) {   // ~1Hz, independent of captured frames —
            last_status = now;                   // this is what makes the satellite
            sat_send_status(boot_us);            // discoverable on a silent channel.
        }
        if (now - last_heartbeat >= 5000000ULL) {   // every ~5s: prove liveness independent of SPI
            last_heartbeat = now;
            ESP_LOGI(TAG, "heartbeat: ch=%u captured=%lu dropped_buf=%lu dropped_spi=%lu",
                     s_cfg.channel, (unsigned long)radio_capture_count(),
                     (unsigned long)radio_capture_dropped_buf(), (unsigned long)s_dropped);
        }
        if (s_reboot_pending) {   // reboot into the freshly-flashed slot
            vTaskDelay(pdMS_TO_TICKS(400));
            esp_restart();
        }
    }
}

#endif // BUILD_SATELLITE
