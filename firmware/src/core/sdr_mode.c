// sdr_mode.c — see sdr_mode.h.
#include "sdr_mode.h"
#include <stdio.h>
#include <string.h>
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "proto.h"
#include "radio_capture.h"
#include "sdr_capture.h"
#include "sdr_spectrum.h"

static const char *TAG = "sdr-mode";

static portMUX_TYPE s_cfg_mux = portMUX_INITIALIZER_UNLOCKED;
static sdr_sweep_config_t s_cfg;
static uint16_t s_interval_ms;
static bool s_cfg_init;
static bool s_failed;   // begin() failed once — don't keep thrashing the radio

typedef struct {
    uint8_t radio_id;
    sdr_emit_fn emit;
    bool dropped;       // an earlier segment didn't make it out
} emit_ctx_t;

static void ensure_cfg(void)
{
    if (s_cfg_init) return;
    sdr_spectrum_default_config(&s_cfg);
    s_interval_ms = 0;
    s_cfg_init = true;
}

bool sdr_mode_configure(const uint8_t *p, uint16_t len)
{
    if (len < 1) return false;
    sdr_sweep_config_t c = {0};
    c.n_lo = p[0];
    if (c.n_lo < 1 || c.n_lo > SDR_MAX_LO || len < 1u + 2u * c.n_lo + 5u) return false;
    size_t o = 1;
    for (unsigned i = 0; i < c.n_lo; i++, o += 2) c.lo_mhz[i] = (uint16_t)(p[o] | (p[o + 1] << 8));
    c.fft_log2 = p[o++];
    c.n_avg    = p[o++];
    c.detector = p[o++];
    uint16_t interval = (uint16_t)(p[o] | (p[o + 1] << 8));
    if (!sdr_spectrum_config_valid(&c)) return false;
    if (SDR_HDR_LEN + (1u << c.fft_log2) > ZB_MAX_TX - ZB_FRAME_OVERHEAD) return false;  // one segment per message
    taskENTER_CRITICAL(&s_cfg_mux);
    s_cfg = c;
    s_interval_ms = interval;
    s_cfg_init = true;
    taskEXIT_CRITICAL(&s_cfg_mux);
    return true;
}

uint8_t sdr_mode_caps(void)
{
    return (sdr_capture_supported() && !s_failed) ? CAP_SDR : 0;
}

static void put_u16(uint8_t *b, uint16_t v) { b[0] = (uint8_t)v; b[1] = (uint8_t)(v >> 8); }
static void put_u32(uint8_t *b, uint32_t v) { for (int i = 0; i < 4; i++) b[i] = (uint8_t)(v >> (8 * i)); }

static void on_segment(const sdr_segment_t *seg, void *arg)
{
    emit_ctx_t *ctx = arg;
    static uint8_t p[SDR_HDR_LEN + (1u << SDR_FFT_LOG2_MAX)];
    uint8_t flags = seg->flags | (ctx->dropped ? SDR_SEG_DROPPED : 0);
    p[0] = ctx->radio_id;
    put_u16(&p[1], seg->sweep_id);
    p[3] = seg->seg_idx;
    p[4] = seg->seg_count;
    put_u16(&p[5], seg->lo_mhz);
    p[7] = seg->fft_log2;
    put_u32(&p[8], seg->sample_rate_hz);
    p[12] = seg->n_ffts;
    p[13] = flags;
    p[14] = seg->gain_min;
    p[15] = seg->gain_max;
    put_u32(&p[16], seg->timestamp_ms);
    p[20] = SDR_CODES_PER_DB;
    p[21] = (uint8_t)(int8_t)SDR_CODE_FLOOR_DBFS;
    memcpy(&p[SDR_HDR_LEN], seg->codes, seg->n_bins);
    ctx->dropped = !ctx->emit(MSG_SDR_SPECTRUM, p, (uint16_t)(SDR_HDR_LEN + seg->n_bins));
}

void sdr_mode_run(uint8_t radio_id, sdr_emit_fn emit)
{
    if (s_failed) { vTaskDelay(pdMS_TO_TICKS(1000)); return; }

    sdr_sweep_config_t cfg;
    uint16_t interval;
    taskENTER_CRITICAL(&s_cfg_mux);
    ensure_cfg();
    cfg = s_cfg;
    interval = s_interval_ms;
    taskEXIT_CRITICAL(&s_cfg_mux);

    const int64_t t0 = esp_timer_get_time();
    radio_lock();
    if (!sdr_capture_active()) {
        radio_capture_suspend();
        esp_err_t err = sdr_capture_begin();
        if (err != ESP_OK) {
            radio_capture_resume();
            radio_unlock();
            s_failed = true;
            char msg[96];
            snprintf(msg, sizeof(msg), "SDR unavailable: %s",
                     err == ESP_ERR_NOT_SUPPORTED ? sdr_capture_unsupported_reason() : esp_err_to_name(err));
            ESP_LOGE(TAG, "%s", msg);
            emit(MSG_LOG, (const uint8_t *)msg, (uint16_t)strlen(msg));
            return;
        }
    }
    static emit_ctx_t ctx;
    ctx.radio_id = radio_id;
    ctx.emit = emit;
    sdr_spectrum_sweep(&cfg, on_segment, &ctx);
    radio_unlock();

    uint32_t elapsed_ms = (uint32_t)((esp_timer_get_time() - t0) / 1000);
    vTaskDelay(pdMS_TO_TICKS(interval > elapsed_ms ? interval - elapsed_ms : 1));
}

void sdr_mode_leave(void)
{
    if (!sdr_capture_active()) return;
    radio_lock();
    sdr_capture_end();
    radio_capture_resume();
    radio_unlock();
}
