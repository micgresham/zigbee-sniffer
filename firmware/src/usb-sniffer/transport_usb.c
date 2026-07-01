// transport_usb.c — see transport_usb.h.
#include "transport_usb.h"
#include "driver/usb_serial_jtag.h"
#include "freertos/FreeRTOS.h"

void transport_usb_init(void)
{
    usb_serial_jtag_driver_config_t cfg = {
        .tx_buffer_size = 4096,
        .rx_buffer_size = 1024,
    };
    usb_serial_jtag_driver_install(&cfg);
}

int transport_usb_write(const uint8_t *data, size_t len)
{
    return usb_serial_jtag_write_bytes(data, len, pdMS_TO_TICKS(100));
}

int transport_usb_read(uint8_t *data, size_t len, uint32_t timeout_ms)
{
    return usb_serial_jtag_read_bytes(data, len, pdMS_TO_TICKS(timeout_ms));
}
