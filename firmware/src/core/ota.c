// ota.c — see ota.h.
#include "ota.h"
#include "proto.h"
#include "esp_ota_ops.h"
#include "esp_log.h"

static const char *TAG = "ota";

static esp_ota_handle_t     s_handle;
static const esp_partition_t *s_part;
static volatile uint8_t     s_state = OTA_IDLE;
static volatile uint32_t    s_recv, s_total;
static volatile uint8_t     s_err;

static esp_err_t fail(uint8_t code, const char *why, esp_err_t e)
{
    ESP_LOGE(TAG, "%s: %s", why, esp_err_to_name(e));
    s_state = OTA_ERROR;
    s_err = code;
    return e;
}

esp_err_t ota_begin(uint32_t total, uint32_t crc32)
{
    (void)crc32; // reserved: esp_ota verifies the image header/signature itself
    s_part = esp_ota_get_next_update_partition(NULL);
    if (!s_part) {
        return fail(1, "no OTA partition (needs an OTA partition table)", ESP_FAIL);
    }
    esp_err_t e = esp_ota_begin(s_part, total ? total : OTA_SIZE_UNKNOWN, &s_handle);
    if (e != ESP_OK) {
        return fail(2, "esp_ota_begin", e);
    }
    s_total = total;
    s_recv = 0;
    s_err = 0;
    s_state = OTA_RECEIVING;
    ESP_LOGI(TAG, "OTA begin: %u bytes → partition %s", (unsigned)total, s_part->label);
    return ESP_OK;
}

esp_err_t ota_write(const uint8_t *data, uint16_t len)
{
    if (s_state != OTA_RECEIVING) {
        return ESP_ERR_INVALID_STATE;
    }
    esp_err_t e = esp_ota_write(s_handle, data, len);
    if (e != ESP_OK) {
        return fail(3, "esp_ota_write", e);
    }
    s_recv += len;
    return ESP_OK;
}

esp_err_t ota_end(void)
{
    if (s_state != OTA_RECEIVING) {
        return ESP_ERR_INVALID_STATE;
    }
    s_state = OTA_VERIFYING;
    esp_err_t e = esp_ota_end(s_handle); // validates the image
    if (e != ESP_OK) {
        return fail(4, "esp_ota_end (bad image)", e);
    }
    e = esp_ota_set_boot_partition(s_part);
    if (e != ESP_OK) {
        return fail(5, "esp_ota_set_boot_partition", e);
    }
    s_state = OTA_OK;
    ESP_LOGI(TAG, "OTA complete — rebooting into %s", s_part->label);
    return ESP_OK;
}

void ota_abort(void)
{
    if (s_state == OTA_RECEIVING) {
        esp_ota_abort(s_handle);
    }
    s_state = OTA_IDLE;
    s_recv = s_total = 0;
    s_err = 0;
}

uint8_t  ota_state(void)    { return s_state; }
uint32_t ota_received(void) { return s_recv; }
uint32_t ota_total(void)    { return s_total; }
uint8_t  ota_err(void)      { return s_err; }
