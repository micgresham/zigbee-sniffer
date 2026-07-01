// web_server.h — HTTP server: serves the embedded lite UI and a /ws WebSocket
// that forwards captured frames/ED/status (binary framing) and accepts commands.
#pragma once

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Called for each command frame received from a browser (already de-framed).
typedef void (*ws_command_cb_t)(uint8_t type, const uint8_t *payload, uint16_t len);

void web_server_start(ws_command_cb_t on_command);

// Broadcast a fully-framed message to all connected WebSocket clients.
void web_server_broadcast(const uint8_t *data, size_t len);

#ifdef __cplusplus
}
#endif
