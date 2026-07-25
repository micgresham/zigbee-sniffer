// spi_master.c — see spi_master.h.
// Built for the standalone aggregator and the tethered relay (OTA to satellites).
#if defined(BUILD_STANDALONE) || defined(BUILD_USB_SNIFFER)

#include "spi_master.h"
#include "codec.h"
#include <string.h>
#include "driver/spi_master.h"
#include "driver/gpio.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "esp_log.h"
#include "esp_attr.h"

static const char *TAG = "spi-pri";
#define XFER 256

static const int s_cs[PRI_SAT_COUNT] = PRI_CS_PINS;
static const int s_dready[PRI_SAT_COUNT] = PRI_DREADY_PINS;
static spi_device_handle_t s_dev[PRI_SAT_COUNT];
static zb_decoder_t s_dec[PRI_SAT_COUNT];
static spi_frame_cb_t s_cb;
static spi_msg_cb_t   s_msg_cb;

// Outbound relay (OTA chunks, channel/start/stop) used to fire its own
// one-off spi_device_transmit() from whichever task called
// spi_master_send_to() — a FULL-DUPLEX SPI transaction, so that call was
// simultaneously receiving whatever the satellite had queued to send back
// (e.g. an OTA_STATUS reply) and silently discarding it, since only
// poll_task's own transactions were ever decoded. During a sustained OTA
// transfer that meant EVERY reply from the satellite was lost, with no
// visible error — poll_task now owns every transaction to this satellite,
// so nothing decodes outside it. spi_master_send_to() just queues.
typedef struct { uint8_t len; uint8_t data[XFER]; } out_item_t;
static QueueHandle_t s_out_q[PRI_SAT_COUNT];
// Given by a satellite's DATA_READY rising edge (ISR) or by
// spi_master_send_to() queuing work, so poll_task reacts immediately instead
// of on its next fixed-interval poll — an interrupt-driven wake rather than a
// blind sleep/recheck loop, while the per-satellite loop below still checks
// every satellite each wake (one shared signal, not one per pin, is enough:
// a spurious extra wake just costs one harmless no-op pass).
static SemaphoreHandle_t s_wake;

static void IRAM_ATTR dready_isr(void *arg)
{
    (void)arg;
    BaseType_t hpw = pdFALSE;
    xSemaphoreGiveFromISR(s_wake, &hpw);
    if (hpw) portYIELD_FROM_ISR();
}

void spi_master_set_msg_cb(spi_msg_cb_t cb) { s_msg_cb = cb; }

// Parse a CAPTURED_FRAME payload into a captured_frame_t (+ radio id).
static bool parse_captured(const uint8_t *p, uint16_t len, uint8_t *radio, captured_frame_t *cf)
{
    if (len < 14) return false;
    *radio = p[0];
    cf->channel = p[1];
    cf->rssi = (int8_t)p[2];
    cf->lqi = p[3];
    cf->flags = p[4];
    cf->timestamp = 0;
    for (int i = 0; i < 8; i++) cf->timestamp |= (uint64_t)p[5 + i] << (8 * i);
    cf->len = p[13];
    if (cf->len > ZB_MAX_PSDU || 14 + cf->len > len) return false;
    memcpy(cf->psdu, &p[14], cf->len);
    return true;
}

static void poll_task(void *arg)
{
    (void)arg;
    WORD_ALIGNED_ATTR static uint8_t tx[XFER];
    WORD_ALIGNED_ATTR static uint8_t rx[XFER];

    static bool seen_dready[PRI_SAT_COUNT];
    for (;;) {
        bool any = false;
        for (int i = 0; i < PRI_SAT_COUNT; i++) {
            out_item_t item;
            bool have_out = xQueueReceive(s_out_q[i], &item, 0) == pdTRUE;
            bool dready = gpio_get_level(s_dready[i]);
            if (!have_out && !dready) continue;
            any = true;
            if (dready && !seen_dready[i]) {
                seen_dready[i] = true;
                ESP_LOGI(TAG, "DATA_READY seen from satellite slot %d (cs=%d dready=%d) — SPI wiring is live",
                         i, s_cs[i], s_dready[i]);
            }
            memset(tx, 0, XFER);
            if (have_out) memcpy(tx, item.data, item.len);
            spi_transaction_t t = { .length = XFER * 8, .tx_buffer = tx, .rx_buffer = rx };
            if (spi_device_transmit(s_dev[i], &t) != ESP_OK) continue;
            // Every transaction is full-duplex — this decodes whatever the
            // satellite sent back, whether this transaction was triggered by
            // its own DATA_READY or by us having something to relay to it
            // (e.g. mid-OTA-transfer, where the two interleave constantly).
            for (int b = 0; b < XFER; b++) {
                uint8_t type; const uint8_t *pl; uint16_t pl_len;
                if (!zb_decoder_push(&s_dec[i], rx[b], &type, &pl, &pl_len)) continue;
                if (type == MSG_CAPTURED_FRAME) {
                    uint8_t radio; captured_frame_t cf;
                    if (parse_captured(pl, pl_len, &radio, &cf)) {
                        // radio_id is fixed by physical wiring slot (1=slot0,
                        // 2=slot1, 3=slot2) — override whatever the satellite
                        // itself reports, so it's never user- or NVS-drifted
                        // out of sync with which CS/DATA_READY wires it's on.
                        if (s_cb) s_cb((uint8_t)(i + 1), &cf);
                    }
                } else {
                    // STATUS carries radio_id as byte 0 too — same fixed override.
                    if (type == MSG_STATUS && pl_len >= 1) ((uint8_t *)pl)[0] = (uint8_t)(i + 1);
                    if (s_msg_cb) s_msg_cb(type, pl, pl_len); // e.g. MSG_OTA_STATUS
                }
            }
        }
        // Interrupt-driven: block until a satellite's DATA_READY line rises
        // or spi_master_send_to() queues outbound work, instead of a fixed
        // poll interval. The timeout is just a safety net (e.g. a missed
        // edge from a level that was already high before an ISR was armed).
        if (!any) xSemaphoreTake(s_wake, pdMS_TO_TICKS(50));
    }
}

void spi_master_init(spi_frame_cb_t cb)
{
    s_cb = cb;
    for (int i = 0; i < PRI_SAT_COUNT; i++) {
        zb_decoder_init(&s_dec[i]);
        s_out_q[i] = xQueueCreate(2, sizeof(out_item_t));
    }
    s_wake = xSemaphoreCreateBinary();

    spi_bus_config_t bus = {
        .mosi_io_num = PRI_PIN_MOSI, .miso_io_num = PRI_PIN_MISO, .sclk_io_num = PRI_PIN_SCK,
        .quadwp_io_num = -1, .quadhd_io_num = -1, .max_transfer_sz = XFER,
    };
    ESP_ERROR_CHECK(spi_bus_initialize(SPI2_HOST, &bus, SPI_DMA_CH_AUTO));

    uint64_t dready_mask = 0;
    for (int i = 0; i < PRI_SAT_COUNT; i++) {
        spi_device_interface_config_t dev = {
            // 200kHz, not 2MHz: our data rate needs are tiny (small, occasional
            // transactions), and breadboard jumper wires have enough parasitic
            // capacitance/inductance to corrupt a 2MHz clock edge even when every
            // wire checks out on a DC continuity test — a very common silent
            // failure mode for prototype SPI wiring.
            .clock_speed_hz = 200 * 1000,
            .mode = 0, .spics_io_num = s_cs[i], .queue_size = 2,
        };
        ESP_ERROR_CHECK(spi_bus_add_device(SPI2_HOST, &dev, &s_dev[i]));
        dready_mask |= (1ULL << s_dready[i]);
    }
    gpio_config_t in = { .pin_bit_mask = dready_mask, .mode = GPIO_MODE_INPUT,
                         .pull_down_en = GPIO_PULLDOWN_ENABLE, .intr_type = GPIO_INTR_POSEDGE };
    gpio_config(&in);
    gpio_install_isr_service(0);
    for (int i = 0; i < PRI_SAT_COUNT; i++) {
        gpio_isr_handler_add(s_dready[i], dready_isr, NULL);
    }

    gpio_config_t sync = { .pin_bit_mask = (1ULL << PRI_PIN_SYNC), .mode = GPIO_MODE_OUTPUT };
    gpio_config(&sync);
    gpio_set_level(PRI_PIN_SYNC, 0);

    xTaskCreate(poll_task, "spi_pri", 4096, NULL, 6, NULL);
    ESP_LOGI(TAG, "SPI master up: interrupt-driven, %d satellites", PRI_SAT_COUNT);
}

void spi_master_send_to(int sat, const uint8_t *data, size_t len)
{
    if (sat < 0 || sat >= PRI_SAT_COUNT || len == 0 || len > XFER) return;
    out_item_t item = { .len = (uint8_t)len };
    memcpy(item.data, data, len);
    // poll_task owns every transaction to this satellite (see its comment) —
    // this just hands off the data and wakes it, rather than transacting
    // (and decoding the reply) itself.
    xQueueSend(s_out_q[sat], &item, portMAX_DELAY);
    xSemaphoreGive(s_wake);
}

void spi_master_set_channel(uint8_t channel)
{
    uint8_t out[ZB_FRAME_OVERHEAD + 1];
    uint8_t payload = channel;
    size_t n = zb_encode(CMD_SET_CHANNEL, &payload, 1, out, sizeof(out));
    if (!n) return;
    for (int i = 0; i < PRI_SAT_COUNT; i++) spi_master_send_to(i, out, n);
}

#endif // BUILD_STANDALONE
