// transport_usb.c — see transport_usb.h.
#include "transport_usb.h"
#include "driver/usb_serial_jtag.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

static SemaphoreHandle_t s_tx_mtx;

void transport_usb_init(void)
{
    usb_serial_jtag_driver_config_t cfg = {
        .tx_buffer_size = 4096,
        .rx_buffer_size = 1024,
    };
    usb_serial_jtag_driver_install(&cfg);
    s_tx_mtx = xSemaphoreCreateMutex();
}

// Two different tasks write here: the main loop (this C6's own captured
// frames) and the SPI-master poll task (frames relayed from a satellite —
// see satellite_frame_cb() in app_main.c). Without a lock, one task's partial
// write can interleave with another's and corrupt both frames; without a
// retry loop, a write that's short because the TX ring buffer is momentarily
// full (a busy local channel keeps it saturated) silently truncates whatever
// the OTHER task was sending — which is exactly what starved satellite
// frames whenever the primary's own radio was also capturing heavily.
int transport_usb_write(const uint8_t *data, size_t len)
{
    xSemaphoreTake(s_tx_mtx, portMAX_DELAY);
    size_t sent = 0;
    while (sent < len) {
        int n = usb_serial_jtag_write_bytes(data + sent, len - sent, pdMS_TO_TICKS(100));
        if (n <= 0) break;   // genuinely stalled (host not draining) — give up rather than spin forever
        sent += (size_t)n;
    }
    xSemaphoreGive(s_tx_mtx);
    return (int)sent;
}

int transport_usb_read(uint8_t *data, size_t len, uint32_t timeout_ms)
{
    return usb_serial_jtag_read_bytes(data, len, pdMS_TO_TICKS(timeout_ms));
}
