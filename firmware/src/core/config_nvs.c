// config_nvs.c — see config_nvs.h.
#include "config_nvs.h"
#include "proto.h"
#include <string.h>
#include "nvs.h"
#include "nvs_flash.h"
#include "esp_log.h"

static const char *TAG = "config";
static const char *NS = "zbsniff";

void config_defaults(device_config_t *cfg)
{
    memset(cfg, 0, sizeof(*cfg));
    cfg->channel = 15;            // common HA/Zigbee channel; override in UI
    cfg->mode = MODE_CAPTURE;
    cfg->radio_id = 0;
    cfg->hop_mask = 0x07FFF800u >> 0; // all of 11..26 set below
    cfg->hop_mask = 0;
    for (uint8_t ch = ZB_CHANNEL_MIN; ch <= ZB_CHANNEL_MAX; ch++)
        cfg->hop_mask |= (1u << (ch - ZB_CHANNEL_MIN));
    cfg->hop_dwell_ms = 0;
    cfg->key_present = false;
    cfg->wifi_sta = false;
    strcpy(cfg->wifi_ssid, "zb-sniffer");
    strcpy(cfg->wifi_pass, "");   // open AP by default; set a password in the UI
}

void config_load(device_config_t *cfg)
{
    config_defaults(cfg);

    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        nvs_flash_erase();
        nvs_flash_init();
    }

    nvs_handle_t h;
    if (nvs_open(NS, NVS_READONLY, &h) != ESP_OK) {
        ESP_LOGI(TAG, "no saved config, using defaults");
        return;
    }
    size_t sz = sizeof(*cfg);
    device_config_t stored;
    if (nvs_get_blob(h, "cfg", &stored, &sz) == ESP_OK && sz == sizeof(*cfg)) {
        *cfg = stored;
        ESP_LOGI(TAG, "config loaded (ch=%u mode=%u radio=%u)",
                 cfg->channel, cfg->mode, cfg->radio_id);
    }
    nvs_close(h);
}

void config_save(const device_config_t *cfg)
{
    nvs_handle_t h;
    if (nvs_open(NS, NVS_READWRITE, &h) != ESP_OK) {
        ESP_LOGE(TAG, "nvs_open failed");
        return;
    }
    nvs_set_blob(h, "cfg", cfg, sizeof(*cfg));
    nvs_commit(h);
    nvs_close(h);
    ESP_LOGI(TAG, "config saved");
}
