// ota.c — see ota.h.
//
// Writes raw bytes straight to the target partition (esp_partition_write)
// instead of using esp_ota_write()'s incremental, stateful image-header
// validation — that validation runs against esp_ota_write()'s own internal
// byte-count tracking, and after many begin/write/abort cycles in one boot
// session (exactly what a flaky transport's retries produce) it started
// rejecting a verified-correct first chunk with ESP_ERR_OTA_VALIDATE_FAILED,
// even though our own end-to-end content checksum (see proto's
// CmdOtaDataMsg) proved the bytes reaching here were byte-for-byte right.
// Bypassing it removes that stateful internal tracking from the picture
// entirely: every write is a plain, stateless byte copy, and the ENTIRE
// image is validated once, in one shot, via esp_image_verify() right before
// switching the boot partition — the same check esp_ota_end() does
// internally, just decoupled from the chunked receive process.
#include "ota.h"
#include "proto.h"
#include "esp_ota_ops.h"
#include "esp_partition.h"
#include "esp_image_format.h"
#include "esp_log.h"

static const char *TAG = "ota";

static const esp_partition_t *s_part;
static volatile uint8_t     s_state = OTA_IDLE;
static volatile uint32_t    s_recv, s_total;
static volatile uint32_t    s_erasedUpTo; // byte offset erased so far (incremental, see ota_write)
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
    (void)crc32; // reserved: esp_image_verify checks the image header/signature itself
    s_part = esp_ota_get_next_update_partition(NULL);
    if (!s_part) {
        return fail(1, "no OTA partition (needs an OTA partition table)", ESP_FAIL);
    }
    if (total == 0 || total > s_part->size) {
        return fail(2, "image size invalid for partition", ESP_ERR_INVALID_SIZE);
    }
    // Deliberately NOT erasing here: a single erase_range() call covering the
    // whole ~250KB image is one long, uninterrupted blocking flash operation
    // (each 4KB sector erase is tens of ms; ~60+ sectors back to back adds up
    // to several seconds with this task never yielding) — on this single-core
    // chip that starves the idle task for the whole call, which is exactly
    // what ESP-IDF's default watchdog monitors, and it resets the device.
    // Erasing per-sector inside ota_write() instead — see there — keeps each
    // individual blocking call short, spread out naturally across the many
    // separate chunk writes.
    s_total = total;
    s_recv = 0;
    s_erasedUpTo = 0;
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
    uint32_t end = s_recv + len;
    if (end > s_erasedUpTo) {
        // Erase just enough new sectors for this chunk — usually one 4KB
        // erase (~tens of ms) or none at all, never the whole image at once.
        uint32_t eraseEnd = (end + 4095) & ~4095u;
        esp_err_t e = esp_partition_erase_range(s_part, s_erasedUpTo, eraseEnd - s_erasedUpTo);
        if (e != ESP_OK) {
            return fail(2, "esp_partition_erase_range", e);
        }
        s_erasedUpTo = eraseEnd;
    }
    esp_err_t e = esp_partition_write(s_part, s_recv, data, len);
    if (e != ESP_OK) {
        return fail(3, "esp_partition_write", e);
    }
    s_recv = end;
    return ESP_OK;
}

esp_err_t ota_end(void)
{
    if (s_state != OTA_RECEIVING) {
        return ESP_ERR_INVALID_STATE;
    }
    s_state = OTA_VERIFYING;
    esp_partition_pos_t pos = { .offset = s_part->address, .size = s_part->size };
    esp_image_metadata_t meta;
    esp_err_t e = esp_image_verify(ESP_IMAGE_VERIFY, &pos, &meta);
    if (e != ESP_OK) {
        return fail(4, "esp_image_verify (bad image)", e);
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
    s_state = OTA_IDLE;
    s_recv = s_total = 0;
    s_err = 0;
}

uint8_t  ota_state(void)    { return s_state; }
uint32_t ota_received(void) { return s_recv; }
uint32_t ota_total(void)    { return s_total; }
uint8_t  ota_err(void)      { return s_err; }
