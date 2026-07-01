# Main component for the firmware. One shared core + three build dirs; the
# active build is selected by the -DBUILD_* flag from platformio.ini, and each
# app_main.c is #if-guarded so only one defines app_main(). (PlatformIO's
# build_src_filter is not honored under the ESP-IDF builder, so we compile all
# sources and let the preprocessor select — see docs/firmware.md.)
idf_component_register(
    SRCS
        "core/codec.c"
        "core/radio_capture.c"
        "core/ed_scan.c"
        "core/probe.c"
        "core/config_nvs.c"
        "usb-sniffer/app_main.c"
        "usb-sniffer/transport_usb.c"
        "standalone/app_main.c"
        "standalone/wifi_ap.c"
        "standalone/web_server.c"
        "standalone/incidents.c"
        "standalone/spi_master.c"
        "standalone/web_assets/web_assets.c"
        "satellite/app_main.c"
        "satellite/transport_spi.c"
    INCLUDE_DIRS
        "core"
        "."
    REQUIRES
        ieee802154
        driver
        nvs_flash
        esp_timer
        log
        esp_wifi
        esp_event
        esp_netif
        esp_http_server
        spiffs
)
