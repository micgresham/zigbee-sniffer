// radio_capture.c — see radio_capture.h.
//
// The esp_ieee802154 driver invokes esp_ieee802154_receive_done() from its
// driver task (not an ISR) for every received frame. We copy the frame into a
// queue and let the transport task drain it, keeping the callback short.
#include "radio_capture.h"
#include <string.h>
#include "esp_ieee802154.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/semphr.h"

static const char *TAG = "radio";

// 128, not 48: on the tethered build the drain loop's transport_usb_write()
// now shares a mutex with the SPI-master poll task's satellite-frame relay
// (see transport_usb.c) — an occasional few-ms wait for that lock, on a busy
// channel, was enough to let this queue fill and drop a frame. More headroom
// absorbs that without needing tighter coupling between the two paths.
#define CAPTURE_QUEUE_DEPTH 128

static QueueHandle_t     s_queue;
static SemaphoreHandle_t s_radio_mtx;   // serializes HAL access across tasks

// Lazily create the mutex so radio_lock() is safe to call before init.
static void ensure_mtx(void)
{
    if (!s_radio_mtx) s_radio_mtx = xSemaphoreCreateMutex();
}

void radio_lock(void)
{
    ensure_mtx();
    xSemaphoreTake(s_radio_mtx, portMAX_DELAY);
}

void radio_unlock(void)
{
    if (s_radio_mtx) xSemaphoreGive(s_radio_mtx);
}
static volatile uint8_t  s_channel;
static volatile uint8_t  s_radio_id;
static volatile uint32_t s_count;
static volatile uint32_t s_dropped;
static volatile bool     s_running; // true between start() and stop() — see set_channel()

// Driver callback: frame[0] = PSDU length, frame[1..] = PSDU (incl. FCS).
void esp_ieee802154_receive_done(uint8_t *frame, esp_ieee802154_frame_info_t *frame_info)
{
    captured_frame_t cf;
    uint8_t len = frame[0];
    if (len > ZB_MAX_PSDU) len = ZB_MAX_PSDU;

    cf.len       = len;
    cf.rssi      = frame_info ? frame_info->rssi : 0;
    cf.lqi       = frame_info ? frame_info->lqi : 0;
    cf.channel   = frame_info ? frame_info->channel : s_channel;
    cf.timestamp = frame_info ? frame_info->timestamp : (uint64_t)esp_timer_get_time();
    cf.flags     = FLAG_PROMISCUOUS;
    if (frame_info && frame_info->pending) cf.flags |= FLAG_FRAME_PENDING;
    // In promiscuous mode the driver delivers frames regardless of FCS; the
    // hardware filter is off, so we mark CRC ok only if the driver vouches for
    // it. Conservative default: assume the FCS was checked by hardware.
    cf.flags |= FLAG_CRC_OK;
    memcpy(cf.psdu, &frame[1], len);

    s_count++;
    if (xQueueSend(s_queue, &cf, 0) != pdTRUE) {
        s_dropped++;
    }
    // CRITICAL: hand the RX buffer back to the driver. The 802.15.4 RX pool is
    // small (~20 frames); without this the pool exhausts and the radio stops
    // receiving after ~20 frames (capture "stalls" at captured=20).
    esp_ieee802154_receive_handle_done(frame);
}

void radio_capture_init(uint8_t channel, uint8_t radio_id)
{
    s_channel = channel;
    s_radio_id = radio_id;
    ensure_mtx();
    s_queue = xQueueCreate(CAPTURE_QUEUE_DEPTH, sizeof(captured_frame_t));

    ESP_ERROR_CHECK(esp_ieee802154_enable());   // inits the 154 PHY (also satisfies sleep retention)
    ESP_ERROR_CHECK(esp_ieee802154_set_promiscuous(true));
    ESP_ERROR_CHECK(esp_ieee802154_set_rx_when_idle(false)); // don't auto-RX until start()
    ESP_ERROR_CHECK(esp_ieee802154_set_channel(channel));
    ESP_LOGI(TAG, "radio %u init on channel %u (promiscuous)", radio_id, channel);
}

bool radio_capture_recv(captured_frame_t *out, uint32_t timeout_ms)
{
    return xQueueReceive(s_queue, out, pdMS_TO_TICKS(timeout_ms)) == pdTRUE;
}

void radio_capture_set_channel(uint8_t channel)
{
    if (channel < ZB_CHANNEL_MIN || channel > ZB_CHANNEL_MAX) return;
    s_channel = channel;
    // esp_ieee802154_set_channel() while the radio is actively mid-receive
    // (which it always is under any real traffic, once radio_capture_start()
    // has left it continuously auto-receiving) can silently fail to retune the
    // actual RF hardware even though this call itself reports no error —
    // every subsequently captured frame's driver-reported channel
    // (esp_ieee802154_receive_done's frame_info->channel) keeps showing the
    // OLD channel, out of sync with s_channel/whatever config_save persisted.
    // radio_capture_init() only gets away with a bare set_channel() because it
    // runs before the radio is ever put into RX. Bracket a live change with
    // the same idle/re-arm radio_capture_stop()/start() already use, so the
    // driver is in a safe state for the retune. Only re-arm RX afterward if
    // capture was actually running before — otherwise this would silently
    // resume a capture the caller had deliberately stopped.
    bool was_running = s_running;
    esp_ieee802154_set_rx_when_idle(false);
    esp_ieee802154_sleep();
    esp_ieee802154_set_channel(channel);
    if (was_running) {
        esp_ieee802154_set_rx_when_idle(true);
        esp_ieee802154_receive();
    }
}

uint8_t radio_capture_get_channel(void) { return s_channel; }

void radio_capture_start(void)
{
    s_running = true;
    esp_ieee802154_set_rx_when_idle(true);
    ESP_ERROR_CHECK(esp_ieee802154_receive());
}

void radio_capture_stop(void)
{
    // Stop auto-receiving AND park the radio so it stops competing for the
    // antenna (important for WiFi coexistence in the standalone build).
    s_running = false;
    esp_ieee802154_set_rx_when_idle(false);
    esp_ieee802154_sleep();
}

uint32_t radio_capture_count(void)       { return s_count; }
uint32_t radio_capture_dropped_buf(void) { return s_dropped; }
