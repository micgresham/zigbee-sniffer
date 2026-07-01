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
#include "esp_log.h"

static const char *TAG = "spi-pri";
#define XFER 256

static const int s_cs[PRI_SAT_COUNT] = PRI_CS_PINS;
static const int s_dready[PRI_SAT_COUNT] = PRI_DREADY_PINS;
static spi_device_handle_t s_dev[PRI_SAT_COUNT];
static zb_decoder_t s_dec[PRI_SAT_COUNT];
static spi_frame_cb_t s_cb;
static spi_msg_cb_t   s_msg_cb;

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
    memset(tx, 0, sizeof(tx));

    for (;;) {
        bool any = false;
        for (int i = 0; i < PRI_SAT_COUNT; i++) {
            if (!gpio_get_level(s_dready[i])) continue;
            any = true;
            spi_transaction_t t = { .length = XFER * 8, .tx_buffer = tx, .rx_buffer = rx };
            if (spi_device_transmit(s_dev[i], &t) != ESP_OK) continue;
            for (int b = 0; b < XFER; b++) {
                uint8_t type; const uint8_t *pl; uint16_t pl_len;
                if (!zb_decoder_push(&s_dec[i], rx[b], &type, &pl, &pl_len)) continue;
                if (type == MSG_CAPTURED_FRAME) {
                    uint8_t radio; captured_frame_t cf;
                    if (parse_captured(pl, pl_len, &radio, &cf) && s_cb) s_cb(radio, &cf);
                } else if (s_msg_cb) {
                    s_msg_cb(type, pl, pl_len); // e.g. MSG_OTA_STATUS from the satellite
                }
            }
        }
        if (!any) vTaskDelay(pdMS_TO_TICKS(2));
    }
}

void spi_master_init(spi_frame_cb_t cb)
{
    s_cb = cb;
    for (int i = 0; i < PRI_SAT_COUNT; i++) zb_decoder_init(&s_dec[i]);

    spi_bus_config_t bus = {
        .mosi_io_num = PRI_PIN_MOSI, .miso_io_num = PRI_PIN_MISO, .sclk_io_num = PRI_PIN_SCK,
        .quadwp_io_num = -1, .quadhd_io_num = -1, .max_transfer_sz = XFER,
    };
    ESP_ERROR_CHECK(spi_bus_initialize(SPI2_HOST, &bus, SPI_DMA_CH_AUTO));

    uint64_t dready_mask = 0;
    for (int i = 0; i < PRI_SAT_COUNT; i++) {
        spi_device_interface_config_t dev = {
            .clock_speed_hz = 2 * 1000 * 1000,   // 2 MHz; ample for 802.15.4 rates
            .mode = 0, .spics_io_num = s_cs[i], .queue_size = 2,
        };
        ESP_ERROR_CHECK(spi_bus_add_device(SPI2_HOST, &dev, &s_dev[i]));
        dready_mask |= (1ULL << s_dready[i]);
    }
    gpio_config_t in = { .pin_bit_mask = dready_mask, .mode = GPIO_MODE_INPUT,
                         .pull_down_en = GPIO_PULLDOWN_ENABLE };
    gpio_config(&in);

    gpio_config_t sync = { .pin_bit_mask = (1ULL << PRI_PIN_SYNC), .mode = GPIO_MODE_OUTPUT };
    gpio_config(&sync);
    gpio_set_level(PRI_PIN_SYNC, 0);

    xTaskCreate(poll_task, "spi_pri", 4096, NULL, 6, NULL);
    ESP_LOGI(TAG, "SPI master up: polling %d satellites", PRI_SAT_COUNT);
}

void spi_master_send_to(int sat, const uint8_t *data, size_t len)
{
    if (sat < 0 || sat >= PRI_SAT_COUNT || len == 0 || len > XFER) return;
    WORD_ALIGNED_ATTR static uint8_t tx[XFER];
    WORD_ALIGNED_ATTR static uint8_t rx[XFER];
    memset(tx, 0, sizeof(tx));
    memcpy(tx, data, len);
    spi_transaction_t t = { .length = XFER * 8, .tx_buffer = tx, .rx_buffer = rx };
    spi_device_transmit(s_dev[sat], &t);
}

void spi_master_set_channel(uint8_t channel)
{
    uint8_t out[ZB_FRAME_OVERHEAD + 1];
    uint8_t payload = channel;
    size_t n = zb_encode(CMD_SET_CHANNEL, &payload, 1, out, sizeof(out));
    if (!n) return;
    WORD_ALIGNED_ATTR static uint8_t tx[XFER];
    WORD_ALIGNED_ATTR static uint8_t rx[XFER];
    memset(tx, 0, sizeof(tx));
    memcpy(tx, out, n);
    for (int i = 0; i < PRI_SAT_COUNT; i++) {
        spi_transaction_t t = { .length = XFER * 8, .tx_buffer = tx, .rx_buffer = rx };
        spi_device_transmit(s_dev[i], &t);
    }
}

#endif // BUILD_STANDALONE
