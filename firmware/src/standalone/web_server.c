// web_server.c — see web_server.h.
#if defined(BUILD_STANDALONE)

#include "web_server.h"
#include "codec.h"
#include "incidents.h"
#include <string.h>
#include "esp_http_server.h"
#include "esp_log.h"

static const char *TAG = "web";
static httpd_handle_t s_server;
static ws_command_cb_t s_on_command;

// Embedded gzipped lite UI — generated C array (web_assets/web_assets.c, built
// by web-lite/build.sh). Compiled as a normal source, so no embed-file magic.
extern const unsigned char index_html_gz[];
extern const unsigned int  index_html_gz_len;

// --- GET / : serve the gzipped single-page UI -------------------------------
static esp_err_t root_handler(httpd_req_t *req)
{
    httpd_resp_set_type(req, "text/html");
    httpd_resp_set_hdr(req, "Content-Encoding", "gzip");
    return httpd_resp_send(req, (const char *)index_html_gz, index_html_gz_len);
}

// --- GET /incidents : the persisted incident log as JSON lines --------------
static esp_err_t incidents_handler(httpd_req_t *req)
{
    static char buf[8192];
    int n = incidents_read_all(buf, sizeof(buf));
    httpd_resp_set_type(req, "application/json");
    httpd_resp_set_hdr(req, "Access-Control-Allow-Origin", "*");
    return httpd_resp_send(req, buf, n > 0 ? n : 0);
}

// --- GET /ws : WebSocket (handshake + inbound command frames) ---------------
static esp_err_t ws_handler(httpd_req_t *req)
{
    if (req->method == HTTP_GET) {
        ESP_LOGI(TAG, "ws client connected (fd=%d)", httpd_req_to_sockfd(req));
        return ESP_OK;  // handshake done
    }

    httpd_ws_frame_t frame = { .type = HTTPD_WS_TYPE_BINARY };
    // First call with len=0 to learn the length.
    esp_err_t r = httpd_ws_recv_frame(req, &frame, 0);
    if (r != ESP_OK || frame.len == 0 || frame.len > 256) return r;

    uint8_t buf[256];
    frame.payload = buf;
    r = httpd_ws_recv_frame(req, &frame, frame.len);
    if (r != ESP_OK) return r;

    // De-frame and dispatch each complete command.
    if (s_on_command) {
        static zb_decoder_t dec;
        for (size_t i = 0; i < frame.len; i++) {
            uint8_t type; const uint8_t *pl; uint16_t pl_len;
            if (zb_decoder_push(&dec, buf[i], &type, &pl, &pl_len)) {
                s_on_command(type, pl, pl_len);
            }
        }
    }
    return ESP_OK;
}

void web_server_start(ws_command_cb_t on_command)
{
    s_on_command = on_command;
    httpd_config_t cfg = HTTPD_DEFAULT_CONFIG();
    cfg.max_open_sockets = 4;
    cfg.lru_purge_enable = true;

    if (httpd_start(&s_server, &cfg) != ESP_OK) {
        ESP_LOGE(TAG, "httpd_start failed");
        return;
    }
    httpd_uri_t root = { .uri = "/", .method = HTTP_GET, .handler = root_handler };
    httpd_uri_t inc = { .uri = "/incidents", .method = HTTP_GET, .handler = incidents_handler };
    httpd_uri_t ws = { .uri = "/ws", .method = HTTP_GET, .handler = ws_handler,
                       .is_websocket = true };
    httpd_register_uri_handler(s_server, &root);
    httpd_register_uri_handler(s_server, &inc);
    httpd_register_uri_handler(s_server, &ws);
    ESP_LOGI(TAG, "http server started");
}

void web_server_broadcast(const uint8_t *data, size_t len)
{
    if (!s_server) return;

    size_t max_clients = 5;
    int client_fds[5];
    if (httpd_get_client_list(s_server, &max_clients, client_fds) != ESP_OK) return;

    httpd_ws_frame_t frame = {
        .type = HTTPD_WS_TYPE_BINARY,
        .payload = (uint8_t *)data,
        .len = len,
    };
    for (size_t i = 0; i < max_clients; i++) {
        int fd = client_fds[i];
        if (httpd_ws_get_fd_info(s_server, fd) == HTTPD_WS_CLIENT_WEBSOCKET) {
            httpd_ws_send_frame_async(s_server, fd, &frame);
        }
    }
}

#endif // BUILD_STANDALONE
