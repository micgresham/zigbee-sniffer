// transport_spi.c — see transport_spi.h.
//
// The satellite is an SPI slave. A byte ring buffers outgoing framed messages;
// a service task hands fixed-size chunks to the SPI driver each time the primary
// (master) clocks a transaction. DATA_READY tells the master we have data. The
// same transaction's MOSI bytes carry de-framed commands back from the master.
#if defined(BUILD_SATELLITE)

#include "transport_spi.h"
#include "codec.h"
#include <string.h>
#include "driver/spi_slave.h"
#include "driver/gpio.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/semphr.h"
#include "esp_log.h"

static const char *TAG = "spi-sat";
#define RING_SIZE 4096

static uint8_t s_ring[RING_SIZE];
static volatile size_t s_head, s_tail;     // head = write, tail = read
static SemaphoreHandle_t s_lock;
static spi_command_cb_t s_cmd_cb;

static size_t ring_used(void)
{
    return (s_head + RING_SIZE - s_tail) % RING_SIZE;
}

bool spi_slave_send(const uint8_t *data, size_t len)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    bool ok = (RING_SIZE - 1 - ring_used()) >= len;
    if (ok) {
        for (size_t i = 0; i < len; i++) {
            s_ring[s_head] = data[i];
            s_head = (s_head + 1) % RING_SIZE;
        }
    }
    xSemaphoreGive(s_lock);
    if (ok) gpio_set_level(SAT_PIN_DATA_READY, 1);
    return ok;
}

static void fill_tx(uint8_t *tx, size_t n)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    for (size_t i = 0; i < n; i++) {
        if (s_tail != s_head) {
            tx[i] = s_ring[s_tail];
            s_tail = (s_tail + 1) % RING_SIZE;
        } else {
            tx[i] = 0x00;   // idle padding (resync magic-scan tolerates it)
        }
    }
    bool empty = (s_tail == s_head);
    xSemaphoreGive(s_lock);
    if (empty) gpio_set_level(SAT_PIN_DATA_READY, 0);
}

static void spi_task(void *arg)
{
    (void)arg;
    WORD_ALIGNED_ATTR static uint8_t tx[SAT_SPI_XFER];
    WORD_ALIGNED_ATTR static uint8_t rx[SAT_SPI_XFER];
    static zb_decoder_t dec;   // ~2 KB — keep off the task stack
    zb_decoder_init(&dec);

    for (;;) {
        fill_tx(tx, SAT_SPI_XFER);
        spi_slave_transaction_t t = {
            .length = SAT_SPI_XFER * 8,
            .tx_buffer = tx,
            .rx_buffer = rx,
        };
        // Blocks until the master clocks a transaction.
        if (spi_slave_transmit(SPI2_HOST, &t, portMAX_DELAY) != ESP_OK) continue;

        // De-frame inbound command bytes from the master.
        for (size_t i = 0; i < SAT_SPI_XFER; i++) {
            uint8_t type; const uint8_t *pl; uint16_t pl_len;
            if (zb_decoder_push(&dec, rx[i], &type, &pl, &pl_len) && s_cmd_cb) {
                s_cmd_cb(type, pl, pl_len);
            }
        }
    }
}

void spi_slave_transport_init(spi_command_cb_t cb)
{
    s_cmd_cb = cb;
    s_lock = xSemaphoreCreateMutex();

    gpio_config_t io = {
        .pin_bit_mask = (1ULL << SAT_PIN_DATA_READY),
        .mode = GPIO_MODE_OUTPUT,
    };
    gpio_config(&io);
    gpio_set_level(SAT_PIN_DATA_READY, 0);

    spi_bus_config_t bus = {
        .mosi_io_num = SAT_PIN_MOSI,
        .miso_io_num = SAT_PIN_MISO,
        .sclk_io_num = SAT_PIN_SCK,
        .quadwp_io_num = -1,
        .quadhd_io_num = -1,
    };
    spi_slave_interface_config_t slv = {
        .spics_io_num = SAT_PIN_CS,
        .flags = 0,
        .queue_size = 3,
        .mode = 0,
    };
    ESP_ERROR_CHECK(spi_slave_initialize(SPI2_HOST, &bus, &slv, SPI_DMA_CH_AUTO));
    xTaskCreate(spi_task, "spi_sat", 4096, NULL, 6, NULL);
    ESP_LOGI(TAG, "SPI slave transport up (cs=%d dready=%d)", SAT_PIN_CS, SAT_PIN_DATA_READY);
}

#endif // BUILD_SATELLITE
