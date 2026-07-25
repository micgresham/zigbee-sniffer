// ed_scan.c — see ed_scan.h.
//
// Uses esp_ieee802154_energy_detect(), whose result arrives via the
// esp_ieee802154_energy_detect_done() callback. We bridge that async callback
// to a synchronous call with a binary semaphore.
#include "ed_scan.h"
#include "proto.h"
#include "radio_capture.h"
#include "esp_ieee802154.h"
#include "esp_timer.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

static const char *TAG = "ed";

// Result sentinels (returned as the int8 "dBm" when no real reading is taken):
#define ED_NONE   ((int8_t)-128)   // callback never fired (timeout)
#define ED_ERR    ((int8_t)-127)   // esp_ieee802154_energy_detect() rejected the call

static SemaphoreHandle_t s_ed_done;
static volatile int8_t   s_ed_result;
static uint16_t          s_sweep_id;
static esp_err_t         s_last_err;

void esp_ieee802154_energy_detect_done(int8_t power)
{
    s_ed_result = power;
    BaseType_t hpw = pdFALSE;
    if (s_ed_done) xSemaphoreGiveFromISR(s_ed_done, &hpw);
    portYIELD_FROM_ISR(hpw);
}

// One channel's energy. NOTE: esp_ieee802154_energy_detect() takes a duration
// in SYMBOLS (1 symbol = 16 us), not microseconds. We run a short fixed ED
// window and repeat it across the requested dwell, keeping the peak reading.
int8_t ed_measure(uint8_t channel, uint32_t duration_us)
{
    if (!s_ed_done) s_ed_done = xSemaphoreCreateBinary();
    radio_capture_set_channel(channel);

    const uint32_t ed_symbols = 8;                 // 8 sym × 16 us = 128 us per sample
    uint32_t samples = duration_us / 200;          // ~one ED sample per 200 us of dwell
    if (samples < 1)  samples = 1;
    if (samples > 64) samples = 64;

    int8_t peak = ED_NONE;
    for (uint32_t i = 0; i < samples; i++) {
        s_ed_result = ED_NONE;
        esp_err_t err = esp_ieee802154_energy_detect(ed_symbols);
        if (err != ESP_OK) {
            s_last_err = err;
            return ED_ERR;                         // distinct sentinel: API rejected
        }
        // ED completes in ~128 us; 30 ms is a very generous ceiling.
        if (xSemaphoreTake(s_ed_done, pdMS_TO_TICKS(30)) == pdTRUE) {
            if (s_ed_result > peak) peak = s_ed_result;
        }
    }
    return peak;
}

uint16_t ed_sweep(uint32_t hop_mask, uint32_t dwell_us, ed_sample_cb_t cb, void *ctx)
{
    uint16_t id = ++s_sweep_id;
    // Remember the capture channel: the sweep walks 11..26 and would otherwise
    // leave the radio parked on channel 26, so capture "resumes" deaf.
    uint8_t saved_ch = radio_capture_get_channel();
    int8_t worst = 0;
    for (uint8_t ch = ZB_CHANNEL_MIN; ch <= ZB_CHANNEL_MAX; ch++) {
        if (!(hop_mask & (1u << (ch - ZB_CHANNEL_MIN)))) continue;
        int8_t ed = ed_measure(ch, dwell_us);
        if (ed == ED_ERR || ed == ED_NONE) worst = ed;
        ed_sample_t s = {
            .channel   = ch,
            .ed_dbm    = ed,
            .sweep_id  = id,
            .timestamp = (uint64_t)esp_timer_get_time(),
        };
        if (cb) cb(&s, ctx);
    }
    if (worst == ED_ERR) {
        ESP_LOGW(TAG, "energy_detect rejected (err 0x%x) — radio busy/bad state", s_last_err);
    } else if (worst == ED_NONE) {
        ESP_LOGW(TAG, "energy_detect timed out — done callback never fired");
    }
    // Restore the capture channel and re-arm RX so capture resumes cleanly.
    radio_capture_set_channel(saved_ch);
    esp_ieee802154_set_rx_when_idle(true);
    esp_ieee802154_receive();
    return id;
}
