// wifi_ap.c — see wifi_ap.h.
#if defined(BUILD_STANDALONE)

#include "wifi_ap.h"
#include <string.h>
#include "esp_wifi.h"
#include "esp_event.h"
#include "esp_netif.h"
#include "esp_log.h"

static const char *TAG = "wifi";
static char s_ip[16] = "192.168.4.1";

static volatile int s_sta_count = 0;

static void on_wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (base != WIFI_EVENT) return;
    if (id == WIFI_EVENT_AP_STACONNECTED) {
        s_sta_count++;
        ESP_LOGI(TAG, "client joined AP (now %d)", s_sta_count);
    } else if (id == WIFI_EVENT_AP_STADISCONNECTED) {
        if (s_sta_count > 0) s_sta_count--;
        ESP_LOGI(TAG, "client left AP (now %d)", s_sta_count);
    }
}

int wifi_sta_count(void) { return s_sta_count; }

static void start_ap(const device_config_t *cfg)
{
    esp_netif_create_default_wifi_ap();

    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init));
    ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID,
                                               &on_wifi_event, NULL));

    wifi_config_t wc = { 0 };
    size_t ssid_len = strlen(cfg->wifi_ssid);
    memcpy(wc.ap.ssid, cfg->wifi_ssid, ssid_len);
    wc.ap.ssid_len = ssid_len;
    wc.ap.channel = 1;            // 2.4 GHz WiFi channel for the AP itself
    wc.ap.max_connection = 4;
    if (strlen(cfg->wifi_pass) >= 8) {
        memcpy(wc.ap.password, cfg->wifi_pass, strlen(cfg->wifi_pass));
        wc.ap.authmode = WIFI_AUTH_WPA2_PSK;
    } else {
        wc.ap.authmode = WIFI_AUTH_OPEN;
    }

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_AP));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_AP, &wc));
    ESP_ERROR_CHECK(esp_wifi_start());
    ESP_LOGI(TAG, "SoftAP '%s' up, UI at http://%s", cfg->wifi_ssid, s_ip);
}

static void start_sta(const device_config_t *cfg)
{
    esp_netif_t *netif = esp_netif_create_default_wifi_sta();

    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    ESP_ERROR_CHECK(esp_wifi_init(&init));

    wifi_config_t wc = { 0 };
    strncpy((char *)wc.sta.ssid, cfg->wifi_ssid, sizeof(wc.sta.ssid) - 1);
    strncpy((char *)wc.sta.password, cfg->wifi_pass, sizeof(wc.sta.password) - 1);

    ESP_ERROR_CHECK(esp_wifi_set_mode(WIFI_MODE_STA));
    ESP_ERROR_CHECK(esp_wifi_set_config(WIFI_IF_STA, &wc));
    ESP_ERROR_CHECK(esp_wifi_start());
    ESP_ERROR_CHECK(esp_wifi_connect());

    // IP is assigned asynchronously by DHCP; the UI is reachable once the STA
    // gets an address (printed by the netif on IP_EVENT). Report a placeholder.
    esp_netif_ip_info_t ip;
    if (esp_netif_get_ip_info(netif, &ip) == ESP_OK && ip.ip.addr) {
        esp_ip4addr_ntoa(&ip.ip, s_ip, sizeof(s_ip));
    } else {
        strcpy(s_ip, "(DHCP pending)");
    }
    ESP_LOGI(TAG, "joining '%s' as STA", cfg->wifi_ssid);
}

const char *wifi_start(const device_config_t *cfg)
{
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());

    if (cfg->wifi_sta) start_sta(cfg);
    else               start_ap(cfg);

    return s_ip;
}

#endif // BUILD_STANDALONE
